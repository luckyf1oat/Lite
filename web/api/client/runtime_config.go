package client

import (
	"github.com/nuomiiiii/lite/database/clients"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func getClientRuntimeConfig(uuid string) (*v2.ConfigParams, error) {
	clientInfo, err := clients.GetClientByUUID(uuid)
	if err != nil {
		return nil, err
	}
	profile, saved, deliveryState, err := clients.GetDeploymentProfileWithDelivery(uuid)
	if err != nil {
		return nil, err
	}
	if saved {
		config := profile.RuntimeConfig()
		config.Revision = deliveryState.Revision
		clients.ApplyResetClock(&config, clientInfo)
		return &config, nil
	}
	if clientInfo.TrafficResetDay == nil {
		return nil, nil
	}
	config := clients.AgentMonthRotateConfig(clientInfo)
	return &config, nil
}
