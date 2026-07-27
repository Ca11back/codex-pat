package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestApplicationWiresManagementRoutes(t *testing.T) {
	shutdownPlugin()
	t.Cleanup(shutdownPlugin)
	application.RLock()
	dispatcherBeforeCall := application.dispatcher
	application.RUnlock()
	if dispatcherBeforeCall != nil {
		t.Fatal("dispatcher initialized before the first plugin call")
	}

	raw := dispatchPlugin(pluginabi.MethodManagementRegister, []byte(`{}`))
	application.RLock()
	dispatcherAfterCall := application.dispatcher
	application.RUnlock()
	if dispatcherAfterCall == nil {
		t.Fatal("dispatcher was not initialized by the first plugin call")
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("registration error = %#v", envelope.Error)
	}
	var registration pluginapi.ManagementRegistrationResponse
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if len(registration.Routes) != 3 || len(registration.Resources) == 0 {
		t.Fatalf("management registration = %#v", registration)
	}
}
