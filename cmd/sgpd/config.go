package main

import (
	"errors"

	"github.com/spf13/viper"
)

var (
	errHarnessTokenRequired    = errors.New("--harness-token is required")
	errManagementTokenRequired = errors.New("--management-token is required")
	errInvalidBearerToken      = errors.New("invalid bearer token")
	errSessionIDRequired       = errors.New("session_id is required")
	errEventRequired           = errors.New("event is required")
	errNodeIDRequired          = errors.New("node_id is required")
)

type config struct {
	StoreBackend    string
	StorePath       string
	HarnessAddr     string
	HarnessToken    string
	ManagementAddr  string
	ManagementToken string
	TLSCert         string
	TLSKey          string
}

func loadConfig(v *viper.Viper) (config, error) {
	cfg := config{
		StoreBackend:    v.GetString("store-backend"),
		StorePath:       v.GetString("store-path"),
		HarnessAddr:     v.GetString("harness-addr"),
		HarnessToken:    v.GetString("harness-token"),
		ManagementAddr:  v.GetString("management-addr"),
		ManagementToken: v.GetString("management-token"),
		TLSCert:         v.GetString("tls-cert"),
		TLSKey:          v.GetString("tls-key"),
	}

	if cfg.StoreBackend == "" {
		cfg.StoreBackend = "bolt"
	}

	if cfg.StorePath == "" {
		cfg.StorePath = "./sgpd-data"
	}

	var errs []error

	if cfg.HarnessToken == "" {
		errs = append(errs, errHarnessTokenRequired)
	}

	if cfg.ManagementToken == "" {
		errs = append(errs, errManagementTokenRequired)
	}

	return cfg, errors.Join(errs...)
}
