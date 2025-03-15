package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/testutil/integration"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestVestingAccountRewards(t *testing.T) {
	t.Parallel()
	f := initFixture(t)

	// Set up fee pool and parameters
	err := f.distrKeeper.FeePool.Set(f.sdkCtx, distrtypes.FeePool{
		CommunityPool: sdk.NewDecCoins(sdk.DecCoin{Denom: "stake", Amount: math.LegacyNewDec(10000)}),
	})
	require.NoError(t, err)
	require.NoError(t, f.distrKeeper.Params.Set(f.sdkCtx, distrtypes.DefaultParams()))

	// Create validator
	validator, err := stakingtypes.NewValidator(f.valAddr.String(), PKS[0], stakingtypes.Description{})
	assert.NilError(t, err)
	commission := stakingtypes.NewCommission(math.LegacyZeroDec(), math.LegacyOneDec(), math.LegacyOneDec())
	validator, err = validator.SetInitialCommission(commission)
	assert.NilError(t, err)
	validator.DelegatorShares = math.LegacyNewDec(100)
	validator.Tokens = math.NewInt(1000000)
	assert.NilError(t, f.stakingKeeper.SetValidator(f.sdkCtx, validator))

	// Set module account coins
	initTokens := f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, int64(1000))
	err = f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, initTokens)))
	require.NoError(t, err)

	// Create vesting account
	vestingAddr := sdk.AccAddress(PKS[1].Address())
	baseAccount := authtypes.NewBaseAccount(vestingAddr, PKS[1], 100, 0)

	// Define vesting parameters
	vestingAmount := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(1000)))
	vestingStartTime := f.sdkCtx.BlockTime().Unix()
	vestingEndTime := vestingStartTime + 100000 // Long enough for the test

	// Create continuous vesting account
	vestingAcc, err := vestingtypes.NewContinuousVestingAccount(baseAccount, vestingAmount, vestingStartTime, vestingEndTime)
	require.NoError(t, err)

	// Set the vesting account in the account keeper
	f.accountKeeper.SetAccount(f.sdkCtx, vestingAcc)

	// Fund the vesting account
	err = f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, vestingAmount)
	require.NoError(t, err)
	err = f.bankKeeper.SendCoinsFromModuleToAccount(f.sdkCtx, distrtypes.ModuleName, vestingAddr, vestingAmount)
	require.NoError(t, err)

	// Delegate tokens from the vesting account
	delTokens := sdk.TokensFromConsensusPower(1, sdk.DefaultPowerReduction)
	validator, issuedShares := validator.AddTokensFromDel(delTokens)

	valBz, err := f.stakingKeeper.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	delegation := stakingtypes.NewDelegation(vestingAddr.String(), validator.GetOperator(), issuedShares)
	require.NoError(t, f.stakingKeeper.SetDelegation(f.sdkCtx, delegation))
	require.NoError(t, f.distrKeeper.SetDelegatorStartingInfo(f.sdkCtx, valBz, vestingAddr, distrtypes.NewDelegatorStartingInfo(2, math.LegacyOneDec(), 20)))

	// Setup validator rewards
	decCoins := sdk.DecCoins{sdk.NewDecCoinFromDec(sdk.DefaultBondDenom, math.LegacyOneDec())}
	historicalRewards := distrtypes.NewValidatorHistoricalRewards(decCoins, 2)
	err = f.distrKeeper.SetValidatorHistoricalRewards(f.sdkCtx, valBz, 2, historicalRewards)
	require.NoError(t, err)

	// Setup current rewards and outstanding rewards
	currentRewards := distrtypes.NewValidatorCurrentRewards(decCoins, 3)
	err = f.distrKeeper.SetValidatorCurrentRewards(f.sdkCtx, f.valAddr, currentRewards)
	require.NoError(t, err)

	valCommission := sdk.DecCoins{
		sdk.NewDecCoinFromDec("stake", math.LegacyNewDec(3).Quo(math.LegacyNewDec(2))),
	}
	err = f.distrKeeper.SetValidatorOutstandingRewards(f.sdkCtx, f.valAddr, distrtypes.ValidatorOutstandingRewards{Rewards: valCommission})
	require.NoError(t, err)

	// Store initial vesting account state
	initialVestingAcc, ok := f.accountKeeper.GetAccount(f.sdkCtx, vestingAddr).(*vestingtypes.ContinuousVestingAccount)
	require.True(t, ok)
	initialOriginalVesting := initialVestingAcc.OriginalVesting
	initialStartTime := initialVestingAcc.StartTime
	initialEndTime := initialVestingAcc.EndTime

	// Withdraw rewards
	msg := &distrtypes.MsgWithdrawDelegatorReward{
		DelegatorAddress: vestingAddr.String(),
		ValidatorAddress: f.valAddr.String(),
	}

	res, err := f.app.RunMsg(
		msg,
		integration.WithAutomaticFinalizeBlock(),
		integration.WithAutomaticCommit(),
	)

	assert.NilError(t, err)
	assert.Assert(t, res != nil)

	// Check the result
	result := distrtypes.MsgWithdrawDelegatorRewardResponse{}
	err = f.cdc.Unmarshal(res.Value, &result)
	assert.NilError(t, err)

	// Verify the vesting account after rewards
	finalVestingAcc, ok := f.accountKeeper.GetAccount(f.sdkCtx, vestingAddr).(*vestingtypes.ContinuousVestingAccount)
	require.True(t, ok)

	// Check that original vesting times are preserved
	assert.Equal(t, initialStartTime, finalVestingAcc.StartTime)
	assert.Equal(t, initialEndTime, finalVestingAcc.EndTime)

	// Check that rewards were received
	finalBalance := f.bankKeeper.GetAllBalances(f.sdkCtx, vestingAddr)
	assert.Assert(t, finalBalance.IsAllGT(vestingAmount))

	// Check that the original vesting amount is unchanged
	assert.DeepEqual(t, initialOriginalVesting, finalVestingAcc.OriginalVesting)

	// Check that delegated free and delegated vesting are properly tracked
	// The delegation should be properly split between vesting and free portions
	assert.Assert(t, !finalVestingAcc.DelegatedVesting.IsZero() || !finalVestingAcc.DelegatedFree.IsZero())
}
