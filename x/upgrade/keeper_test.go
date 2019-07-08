package upgrade

import (
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/suite"
	abci "github.com/tendermint/tendermint/abci/types"
	dbm "github.com/tendermint/tendermint/libs/db"
	"github.com/tendermint/tendermint/libs/log"
)

type TestSuite struct {
	suite.Suite
	keeper Keeper
	ctx    sdk.Context
	cms    store.CommitMultiStore
}

func (s *TestSuite) SetupTest() {
	db := dbm.NewMemDB()
	s.cms = store.NewCommitMultiStore(db)
	key := sdk.NewKVStoreKey("upgrade")
	cdc := codec.New()
	RegisterCodec(cdc)
	s.keeper = NewKeeper(key, cdc)
	s.cms.MountStoreWithDB(key, sdk.StoreTypeIAVL, db)
	_ = s.cms.LoadLatestVersion()
	s.ctx = sdk.NewContext(s.cms, abci.Header{Height: 10, Time: time.Now()}, false, log.NewNopLogger())
}

func (s *TestSuite) TestRequireName() {
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{})
	s.Require().NotNil(err)
	s.Require().Equal(sdk.CodeUnknownRequest, err.Code())
}

func (s *TestSuite) TestRequireFutureTime() {
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Time: s.ctx.BlockHeader().Time})
	s.Require().NotNil(err)
	s.Require().Equal(sdk.CodeUnknownRequest, err.Code())
}

func (s *TestSuite) TestRequireFutureBlock() {
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Height: s.ctx.BlockHeight()})
	s.Require().NotNil(err)
	s.Require().Equal(sdk.CodeUnknownRequest, err.Code())
}

func (s *TestSuite) TestCantSetBothTimeAndHeight() {
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Time: time.Now(), Height: s.ctx.BlockHeight() + 1})
	s.Require().NotNil(err)
	s.Require().Equal(sdk.CodeUnknownRequest, err.Code())
}

func (s *TestSuite) TestDoTimeUpgrade() {
	s.T().Log("Verify can schedule an upgrade")
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Time: time.Now()})
	s.Require().Nil(err)

	s.VerifyDoUpgrade()
}

func (s *TestSuite) TestDoHeightUpgrade() {
	s.T().Log("Verify can schedule an upgrade")
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Height: s.ctx.BlockHeight() + 1})
	s.Require().Nil(err)

	s.VerifyDoUpgrade()
}

func (s *TestSuite) VerifyDoUpgrade() {
	s.T().Log("Verify that a panic happens at the upgrade time/height")
	newCtx := sdk.NewContext(s.cms, abci.Header{Height: s.ctx.BlockHeight() + 1, Time: time.Now()}, false, log.NewNopLogger())
	req := abci.RequestBeginBlock{Header: newCtx.BlockHeader()}
	s.Require().Panics(func() {
		s.keeper.BeginBlocker(newCtx, req)
	})

	s.T().Log("Verify that the upgrade can be successfully applied with a handler")
	s.keeper.SetUpgradeHandler("test", func(ctx sdk.Context, plan Plan) {})
	s.Require().NotPanics(func() {
		s.keeper.BeginBlocker(newCtx, req)
	})

	s.VerifyCleared(newCtx)
}

func (s *TestSuite) VerifyCleared(newCtx sdk.Context) {
	s.T().Log("Verify that the upgrade plan has been cleared")
	_, havePlan := s.keeper.GetUpgradePlan(newCtx)
	s.Require().False(havePlan)
}

func (s *TestSuite) TestCanClear() {
	s.T().Log("Verify upgrade is scheduled")
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Time: time.Now()})
	s.Require().Nil(err)

	s.keeper.ClearUpgradePlan(s.ctx)

	s.VerifyCleared(s.ctx)
}

func (s *TestSuite) TestCantApplySameUpgradeTwice() {
	s.TestDoTimeUpgrade()
	s.T().Log("Verify an upgrade named \"test\" can't be scheduled twice")
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Time: time.Now()})
	s.Require().NotNil(err)
	s.Require().Equal(sdk.CodeUnknownRequest, err.Code())
}

func (s *TestSuite) TestCustomCallbacks() {
	s.T().Log("Set custom WillUpgrader and OnUpgrader")
	onUpgraderCalled := false
	s.keeper.SetOnUpgrader(func(ctx sdk.Context, plan Plan) {
		onUpgraderCalled = true
	})

	s.T().Log("Run an upgrade and verify that the custom OnUpgrader is called")
	err := s.keeper.ScheduleUpgrade(s.ctx, Plan{Name: "test", Height: s.ctx.BlockHeight() + 2})
	s.Require().Nil(err)

	header := abci.Header{Height: s.ctx.BlockHeight() + 1}
	newCtx := sdk.NewContext(s.cms, header, false, log.NewNopLogger())
	req := abci.RequestBeginBlock{Header: header}
	s.Require().NotPanics(func() {
		s.keeper.BeginBlocker(newCtx, req)
	})
	s.Require().False(onUpgraderCalled)

	header = abci.Header{Height: s.ctx.BlockHeight() + 2}
	newCtx = sdk.NewContext(s.cms, header, false, log.NewNopLogger())
	req = abci.RequestBeginBlock{Header: header}
	s.Require().Panics(func() {
		s.keeper.BeginBlocker(newCtx, req)
	})
	s.Require().True(onUpgraderCalled)
}

func (s *TestSuite) TestNoSpuriousUpgrades() {
	s.T().Log("Verify that no upgrade panic is triggered in the BeginBlocker when we haven't scheduled an upgrade")
	req := abci.RequestBeginBlock{Header: s.ctx.BlockHeader()}
	s.Require().NotPanics(func() {
		s.keeper.BeginBlocker(s.ctx, req)
	})
}

func (s *TestSuite) TestPlanStringer() {
	t, err := time.Parse(time.RFC3339, "2020-01-01T00:00:00Z")
	s.Require().Nil(err)
	s.Require().Equal(`Upgrade Plan
  Name: test
  Time: 2020-01-01T00:00:00Z
  Info: `, Plan{Name: "test", Time: t}.String())
	s.Require().Equal(`Upgrade Plan
  Name: test
  Height: 100
  Info: `, Plan{Name: "test", Height: 100}.String())
}

func TestTestSuite(t *testing.T) {
	suite.Run(t, new(TestSuite))
}
