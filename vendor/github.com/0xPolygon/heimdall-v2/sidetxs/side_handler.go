package sidetxs

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc"
)

// SideTxHandler defines the core of the app side-tx execution
type SideTxHandler func(ctx sdk.Context, msg sdk.Msg) Vote

// PostTxHandler defines the core of the app state transition function after side-tx execution
type PostTxHandler func(ctx sdk.Context, msg sdk.Msg, sideTxResult Vote) error

// SideMsgServer defines the interface to implement the side txs and post-handlers.
type SideMsgServer interface {
	// SideTxHandler to register specific sideHandler based on methodName
	SideTxHandler(methodName string) SideTxHandler

	// PostTxHandler to register the specific postHandler based on methodName
	PostTxHandler(methodName string) PostTxHandler
}

func CommonRegisterSideMsgServer(
	sideCfg SideTxConfigurator,
	srv SideMsgServer,
	serviceDesc grpc.ServiceDesc,
) {
	for _, service := range serviceDesc.Methods {

		var requestTypeName string

		// NOTE: This is how we pull the concrete request type for each handler for registering in the InterfaceRegistry.
		// This approach is maybe a bit hacky, but less hacky than reflecting on the handler object itself.
		// We use a no-op interceptor to avoid actually calling into the handler itself.
		_, _ = service.Handler(nil, context.Background(), func(i interface{}) error {
			msg, ok := i.(sdk.Msg)
			if !ok {
				// We panic here because there is no other alternative and the app cannot be initialized correctly.
				// This should only happen if there is a problem with code generation, where the app won't
				// work correctly anyway.
				panic(fmt.Errorf("unable to register service method : %T does not implement sdk.Msg", i))
			}

			requestTypeName = sdk.MsgTypeURL(msg)
			return nil
		}, noopInterceptor)

		sideHandler := srv.SideTxHandler(requestTypeName)

		postHandler := srv.PostTxHandler(requestTypeName)

		if sideHandler == nil && postHandler == nil {
			continue
		}

		err := sideCfg.RegisterSideHandler(requestTypeName, sideHandler)
		if err != nil {
			panic("error in registering the side handler")
		}
		err = sideCfg.RegisterPostHandler(requestTypeName, postHandler)
		if err != nil {
			panic("error in registering the post handler")
		}
	}
}

func noopInterceptor(_ context.Context, _ interface{}, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (interface{}, error) {
	return new(interface{}), nil
}
