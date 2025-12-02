package sidetxs

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// SideTxConfigurator provides the hooks to allow modules to configure and register
// their sideMsg services in the RegisterSideHandler and RegisterPostHandler method.
type SideTxConfigurator interface {
	RegisterSideHandler(msgURL string, handler SideTxHandler) error

	RegisterPostHandler(msgURL string, handler PostTxHandler) error

	GetSideHandler(msg sdk.Msg) SideTxHandler

	GetPostHandler(msg sdk.Msg) PostTxHandler
}

type sideTxConfigurator struct {
	// sideHandlers to register sideHandler against the msgURl string value
	sideHandlers map[string]SideTxHandler

	// postHandlers to register postHandler against the msgURl string value
	postHandlers map[string]PostTxHandler
}

// NewSideTxConfigurator returns a new Configurator instance
func NewSideTxConfigurator() SideTxConfigurator {
	return &sideTxConfigurator{
		sideHandlers: make(map[string]SideTxHandler),
		postHandlers: make(map[string]PostTxHandler),
	}
}

// RegisterSideHandler implements the SideTxConfigurator.RegisterSideHandler method
func (c *sideTxConfigurator) RegisterSideHandler(msgURL string, handler SideTxHandler) error {
	if _, ok := c.sideHandlers[msgURL]; !ok {
		c.sideHandlers[msgURL] = handler
		return nil
	}

	return fmt.Errorf("sideHandler corresponding to the following msg %s already exists", msgURL)
}

// RegisterPostHandler implements the SideTxConfigurator.RegisterPostHandler method
func (c *sideTxConfigurator) RegisterPostHandler(msgURL string, handler PostTxHandler) error {
	if _, ok := c.postHandlers[msgURL]; !ok {
		c.postHandlers[msgURL] = handler
		return nil
	}

	return fmt.Errorf("postHandler corresponding to the following msg %s already exists", msgURL)
}

// GetSideHandler returns SideTxHandler for a given msg or nil if not found.
func (c *sideTxConfigurator) GetSideHandler(msg sdk.Msg) SideTxHandler {
	return c.sideHandlers[sdk.MsgTypeURL(msg)]
}

// GetPostHandler returns PostTxHandler for a given msg or nil if not found.
func (c *sideTxConfigurator) GetPostHandler(msg sdk.Msg) PostTxHandler {
	return c.postHandlers[sdk.MsgTypeURL(msg)]
}

// HasSideMsgServices is the interface for modules to register sideTx services.
type HasSideMsgServices interface {
	// RegisterSideMsgServices allows a module to register side msg services.
	RegisterSideMsgServices(SideTxConfigurator)
}
