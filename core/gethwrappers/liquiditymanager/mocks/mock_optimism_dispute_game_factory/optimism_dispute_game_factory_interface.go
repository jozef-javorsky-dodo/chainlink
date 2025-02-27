// Code generated by mockery v2.52.3. DO NOT EDIT.

package mock_optimism_dispute_game_factory

import (
	big "math/big"

	bind "github.com/ethereum/go-ethereum/accounts/abi/bind"
	common "github.com/ethereum/go-ethereum/common"

	mock "github.com/stretchr/testify/mock"

	optimism_dispute_game_factory "github.com/smartcontractkit/chainlink/v2/core/gethwrappers/liquiditymanager/generated/optimism_dispute_game_factory"
)

// OptimismDisputeGameFactoryInterface is an autogenerated mock type for the OptimismDisputeGameFactoryInterface type
type OptimismDisputeGameFactoryInterface struct {
	mock.Mock
}

type OptimismDisputeGameFactoryInterface_Expecter struct {
	mock *mock.Mock
}

func (_m *OptimismDisputeGameFactoryInterface) EXPECT() *OptimismDisputeGameFactoryInterface_Expecter {
	return &OptimismDisputeGameFactoryInterface_Expecter{mock: &_m.Mock}
}

// Address provides a mock function with no fields
func (_m *OptimismDisputeGameFactoryInterface) Address() common.Address {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Address")
	}

	var r0 common.Address
	if rf, ok := ret.Get(0).(func() common.Address); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(common.Address)
		}
	}

	return r0
}

// OptimismDisputeGameFactoryInterface_Address_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'Address'
type OptimismDisputeGameFactoryInterface_Address_Call struct {
	*mock.Call
}

// Address is a helper method to define mock.On call
func (_e *OptimismDisputeGameFactoryInterface_Expecter) Address() *OptimismDisputeGameFactoryInterface_Address_Call {
	return &OptimismDisputeGameFactoryInterface_Address_Call{Call: _e.mock.On("Address")}
}

func (_c *OptimismDisputeGameFactoryInterface_Address_Call) Run(run func()) *OptimismDisputeGameFactoryInterface_Address_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run()
	})
	return _c
}

func (_c *OptimismDisputeGameFactoryInterface_Address_Call) Return(_a0 common.Address) *OptimismDisputeGameFactoryInterface_Address_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *OptimismDisputeGameFactoryInterface_Address_Call) RunAndReturn(run func() common.Address) *OptimismDisputeGameFactoryInterface_Address_Call {
	_c.Call.Return(run)
	return _c
}

// FindLatestGames provides a mock function with given fields: opts, _gameType, _start, _n
func (_m *OptimismDisputeGameFactoryInterface) FindLatestGames(opts *bind.CallOpts, _gameType uint32, _start *big.Int, _n *big.Int) ([]optimism_dispute_game_factory.IOptimismDisputeGameFactoryGameSearchResult, error) {
	ret := _m.Called(opts, _gameType, _start, _n)

	if len(ret) == 0 {
		panic("no return value specified for FindLatestGames")
	}

	var r0 []optimism_dispute_game_factory.IOptimismDisputeGameFactoryGameSearchResult
	var r1 error
	if rf, ok := ret.Get(0).(func(*bind.CallOpts, uint32, *big.Int, *big.Int) ([]optimism_dispute_game_factory.IOptimismDisputeGameFactoryGameSearchResult, error)); ok {
		return rf(opts, _gameType, _start, _n)
	}
	if rf, ok := ret.Get(0).(func(*bind.CallOpts, uint32, *big.Int, *big.Int) []optimism_dispute_game_factory.IOptimismDisputeGameFactoryGameSearchResult); ok {
		r0 = rf(opts, _gameType, _start, _n)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]optimism_dispute_game_factory.IOptimismDisputeGameFactoryGameSearchResult)
		}
	}

	if rf, ok := ret.Get(1).(func(*bind.CallOpts, uint32, *big.Int, *big.Int) error); ok {
		r1 = rf(opts, _gameType, _start, _n)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// OptimismDisputeGameFactoryInterface_FindLatestGames_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'FindLatestGames'
type OptimismDisputeGameFactoryInterface_FindLatestGames_Call struct {
	*mock.Call
}

// FindLatestGames is a helper method to define mock.On call
//   - opts *bind.CallOpts
//   - _gameType uint32
//   - _start *big.Int
//   - _n *big.Int
func (_e *OptimismDisputeGameFactoryInterface_Expecter) FindLatestGames(opts interface{}, _gameType interface{}, _start interface{}, _n interface{}) *OptimismDisputeGameFactoryInterface_FindLatestGames_Call {
	return &OptimismDisputeGameFactoryInterface_FindLatestGames_Call{Call: _e.mock.On("FindLatestGames", opts, _gameType, _start, _n)}
}

func (_c *OptimismDisputeGameFactoryInterface_FindLatestGames_Call) Run(run func(opts *bind.CallOpts, _gameType uint32, _start *big.Int, _n *big.Int)) *OptimismDisputeGameFactoryInterface_FindLatestGames_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*bind.CallOpts), args[1].(uint32), args[2].(*big.Int), args[3].(*big.Int))
	})
	return _c
}

func (_c *OptimismDisputeGameFactoryInterface_FindLatestGames_Call) Return(_a0 []optimism_dispute_game_factory.IOptimismDisputeGameFactoryGameSearchResult, _a1 error) *OptimismDisputeGameFactoryInterface_FindLatestGames_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *OptimismDisputeGameFactoryInterface_FindLatestGames_Call) RunAndReturn(run func(*bind.CallOpts, uint32, *big.Int, *big.Int) ([]optimism_dispute_game_factory.IOptimismDisputeGameFactoryGameSearchResult, error)) *OptimismDisputeGameFactoryInterface_FindLatestGames_Call {
	_c.Call.Return(run)
	return _c
}

// GameCount provides a mock function with given fields: opts
func (_m *OptimismDisputeGameFactoryInterface) GameCount(opts *bind.CallOpts) (*big.Int, error) {
	ret := _m.Called(opts)

	if len(ret) == 0 {
		panic("no return value specified for GameCount")
	}

	var r0 *big.Int
	var r1 error
	if rf, ok := ret.Get(0).(func(*bind.CallOpts) (*big.Int, error)); ok {
		return rf(opts)
	}
	if rf, ok := ret.Get(0).(func(*bind.CallOpts) *big.Int); ok {
		r0 = rf(opts)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*big.Int)
		}
	}

	if rf, ok := ret.Get(1).(func(*bind.CallOpts) error); ok {
		r1 = rf(opts)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// OptimismDisputeGameFactoryInterface_GameCount_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GameCount'
type OptimismDisputeGameFactoryInterface_GameCount_Call struct {
	*mock.Call
}

// GameCount is a helper method to define mock.On call
//   - opts *bind.CallOpts
func (_e *OptimismDisputeGameFactoryInterface_Expecter) GameCount(opts interface{}) *OptimismDisputeGameFactoryInterface_GameCount_Call {
	return &OptimismDisputeGameFactoryInterface_GameCount_Call{Call: _e.mock.On("GameCount", opts)}
}

func (_c *OptimismDisputeGameFactoryInterface_GameCount_Call) Run(run func(opts *bind.CallOpts)) *OptimismDisputeGameFactoryInterface_GameCount_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*bind.CallOpts))
	})
	return _c
}

func (_c *OptimismDisputeGameFactoryInterface_GameCount_Call) Return(_a0 *big.Int, _a1 error) *OptimismDisputeGameFactoryInterface_GameCount_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *OptimismDisputeGameFactoryInterface_GameCount_Call) RunAndReturn(run func(*bind.CallOpts) (*big.Int, error)) *OptimismDisputeGameFactoryInterface_GameCount_Call {
	_c.Call.Return(run)
	return _c
}

// NewOptimismDisputeGameFactoryInterface creates a new instance of OptimismDisputeGameFactoryInterface. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewOptimismDisputeGameFactoryInterface(t interface {
	mock.TestingT
	Cleanup(func())
}) *OptimismDisputeGameFactoryInterface {
	mock := &OptimismDisputeGameFactoryInterface{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
