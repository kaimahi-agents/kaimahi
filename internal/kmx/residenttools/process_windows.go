//go:build windows

package residenttools

import (
	"context"
	"errors"
	"os"
)

const BrokerProcessEnv = "KMX_LOCAL_TOOL_BROKER"

func IsBrokerProcess() bool { return false }

func Start(context.Context, string, Plan) (*os.File, error) {
	return nil, errors.New("resident tool brokers require Unix or WSL")
}

func Run(*os.File, *os.File, *os.File) error {
	return errors.New("resident tool brokers require Unix or WSL")
}
