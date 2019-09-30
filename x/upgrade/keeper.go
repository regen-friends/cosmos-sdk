package upgrade

import (
	"encoding/binary"
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	abci "github.com/tendermint/tendermint/abci/types"
)

const (
	// ModuleName is the name of this module
	ModuleName = "upgrade"
	// StoreKey is the prefix under which we store this module's data
	StoreKey = ModuleName
)

// Keeper of the upgrade module
type Keeper interface {
	// ScheduleUpgrade schedules an upgrade based on the specified plan
	ScheduleUpgrade(ctx sdk.Context, plan Plan) sdk.Error

	// SetUpgradeHandler sets an UpgradeHandler for the upgrade specified by name. This handler will be called when the upgrade
	// with this name is applied. In order for an upgrade with the given name to proceed, a handler for this upgrade
	// must be set even if it is a no-op function.
	SetUpgradeHandler(name string, upgradeHandler Handler)

	// ClearUpgradePlan clears any schedule upgrade
	ClearUpgradePlan(ctx sdk.Context)

	// GetUpgradePlan returns the currently scheduled Plan if any, setting havePlan to true if there is a scheduled
	// upgrade or false if there is none
	GetUpgradePlan(ctx sdk.Context) (plan Plan, havePlan bool)

	// BeginBlocker should be called inside the BeginBlocker method of any app using the upgrade module. Scheduled upgrade
	// plans are cached in memory so the overhead of this method is trivial.
	BeginBlocker(ctx sdk.Context, req abci.RequestBeginBlock)
}

type keeper struct {
	storeKey        sdk.StoreKey
	cdc             *codec.Codec
	upgradeHandlers map[string]Handler
	haveCache       bool
}

const (
	// PlanByte specifies the Byte under which a pending upgrade plan is stored in the store
	PlanByte = 0x0
	// DoneByte is a prefix for to look up completed upgrade plan by name
	DoneByte = 0x1
)

// PlanKey is the key under which the current plan is saved
// We store PlanByte as a const to keep it immutible (unlike a []byte)
func PlanKey() []byte {
	return []byte{PlanByte}
}

// DoneHeightKey returns a key that points to the height at which a past upgrade was applied
func DoneHeightKey(name string) []byte {
	return append([]byte{DoneByte}, []byte(name)...)
}

// NewKeeper constructs an upgrade keeper
func NewKeeper(storeKey sdk.StoreKey, cdc *codec.Codec) Keeper {
	return &keeper{
		storeKey:        storeKey,
		cdc:             cdc,
		upgradeHandlers: map[string]Handler{},
	}
}

// SetUpgradeHandler sets an UpgradeHandler for the upgrade specified by name. This handler will be called when the upgrade
// with this name is applied. In order for an upgrade with the given name to proceed, a handler for this upgrade
// must be set even if it is a no-op function.
func (keeper *keeper) SetUpgradeHandler(name string, upgradeHandler Handler) {
	keeper.upgradeHandlers[name] = upgradeHandler
}

// ScheduleUpgrade schedules an upgrade based on the specified plan
func (keeper *keeper) ScheduleUpgrade(ctx sdk.Context, plan Plan) sdk.Error {
	err := plan.ValidateBasic()
	if err != nil {
		return err
	}
	if !plan.Time.IsZero() {
		if !plan.Time.After(ctx.BlockHeader().Time) {
			return sdk.ErrUnknownRequest("Upgrade cannot be scheduled in the past")
		}
		if plan.Height != 0 {
			return sdk.ErrUnknownRequest("Only one of Time or Height should be specified")
		}
	} else if plan.Height <= ctx.BlockHeight() {
		return sdk.ErrUnknownRequest("Upgrade cannot be scheduled in the past")
	}
	store := ctx.KVStore(keeper.storeKey)
	if store.Has(DoneHeightKey(plan.Name)) {
		return sdk.ErrUnknownRequest(fmt.Sprintf("Upgrade with name %s has already been completed", plan.Name))
	}
	bz := keeper.cdc.MustMarshalBinaryBare(plan)
	keeper.haveCache = false
	store.Set(PlanKey(), bz)
	return nil
}

// ClearUpgradePlan clears any schedule upgrade
func (keeper *keeper) ClearUpgradePlan(ctx sdk.Context) {
	store := ctx.KVStore(keeper.storeKey)
	keeper.haveCache = false
	store.Delete(PlanKey())
}

// ValidateBasic does basic validation of a Plan
func (plan Plan) ValidateBasic() sdk.Error {
	if len(plan.Name) == 0 {
		return sdk.ErrUnknownRequest("Name cannot be empty")
	}
	return nil
}

// GetUpgradePlan returns the currently scheduled Plan if any, setting havePlan to true if there is a scheduled
// upgrade or false if there is none
func (keeper *keeper) GetUpgradePlan(ctx sdk.Context) (plan Plan, havePlan bool) {
	store := ctx.KVStore(keeper.storeKey)
	bz := store.Get(PlanKey())
	if bz == nil {
		return plan, false
	}
	keeper.cdc.MustUnmarshalBinaryBare(bz, &plan)
	return plan, true
}

// setDone marks this upgrade name as being done so the name can't be reused accidentally
func (keeper *keeper) setDone(ctx sdk.Context, name string) {
	store := ctx.KVStore(keeper.storeKey)
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, uint64(ctx.BlockHeight()))
	store.Set(DoneHeightKey(name), bz)
}

// BeginBlocker should be called inside the BeginBlocker method of any app using the upgrade module. Scheduled upgrade
// plans are cached in memory so the overhead of this method is trivial.
func (keeper *keeper) BeginBlocker(ctx sdk.Context, req abci.RequestBeginBlock) {
	blockTime := ctx.BlockHeader().Time
	blockHeight := ctx.BlockHeight()

	plan, found := keeper.GetUpgradePlan(ctx)
	if !found {
		return
	}

	upgradeTime := plan.Time
	upgradeHeight := plan.Height
	if (!upgradeTime.IsZero() && !blockTime.Before(upgradeTime)) || (upgradeHeight > 0 && upgradeHeight <= blockHeight) {
		handler, ok := keeper.upgradeHandlers[plan.Name]
		if ok {
			// We have an upgrade handler for this upgrade name, so apply the upgrade
			ctx.Logger().Info(fmt.Sprintf("Applying upgrade \"%s\" at height %d", plan.Name, blockHeight))
			handler(ctx, plan)
			keeper.ClearUpgradePlan(ctx)
			keeper.setDone(ctx, plan.Name)
		} else {
			// We don't have an upgrade handler for this upgrade name, meaning this software is out of date so shutdown
			ctx.Logger().Error(fmt.Sprintf("UPGRADE \"%s\" NEEDED at height %d: %s", plan.Name, blockHeight, plan.Info))
			panic("UPGRADE REQUIRED!")
		}
	} else {
		// if we have a pending upgrade, but it is not yet time, make sure we did not
		// set the handler already
		_, ok := keeper.upgradeHandlers[plan.Name]
		if ok {
			ctx.Logger().Error(fmt.Sprintf("UNKNOWN UPGRADE \"%s\" - in binary but not executed on chain", plan.Name))
			panic("BINARY UPDATED BEFORE TRIGGER!")
		}
	}
}
