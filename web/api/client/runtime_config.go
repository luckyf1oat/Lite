package client

import (
	"github.com/nuomiiiii/lite/database/clients"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func getClientRuntimeConfig(uuid string) (*v2.ConfigParams, error) {
	return clients.RuntimeConfigForAgent(uuid)
}
