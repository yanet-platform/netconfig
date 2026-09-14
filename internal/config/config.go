// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yanet-platform/netconfig/internal/bootstrap"
	"github.com/yanet-platform/netconfig/internal/desired"
	"github.com/yanet-platform/netconfig/internal/native"
	"github.com/yanet-platform/netconfig/internal/netplan"
)

// Config selects exactly one interface source and the bootstrap retry policy.
type Config struct {
	Source      string           `yaml:"source"`
	NetplanPath string           `yaml:"netplan_path"`
	Native      *native.Config   `yaml:"native"`
	Retry       bootstrap.Config `yaml:"retry"`
}

// Parse accepts one strict configuration document with bounded retry defaults.
func Parse(data []byte) (*Config, error) {
	config := &Config{Retry: bootstrap.Config{
		InitialBackoff: 250 * time.Millisecond, MaxBackoff: 30 * time.Second,
	}}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("configuration must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing configuration: %w", err)
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var fields map[string]yaml.Node
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if value, present := fields["native"]; present && (config.Source != "native" || value.Kind != yaml.MappingNode) {
		return nil, errors.New("native must be a mapping used only with source: native")
	}
	if value, present := fields["netplan_path"]; present &&
		(config.Source != "netplan" || value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "") {
		return nil, errors.New("netplan_path must be a nonempty string used only with source: netplan")
	}
	if value, present := fields["retry"]; present {
		if value.Kind != yaml.MappingNode {
			return nil, errors.New("retry must be a mapping")
		}
		for idx := range len(value.Content) / 2 {
			if value.Content[2*idx+1].Tag != "!!str" {
				return nil, errors.New("retry delays must be duration strings")
			}
		}
	}
	return config, nil
}

// ParseFile reads the startup config without opening kernel resources.
func ParseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration %q: %w", path, err)
	}
	return Parse(data)
}

// Validate rejects ambiguous sources and invalid retry delays.
func (m *Config) Validate() error {
	if m == nil {
		return errors.New("configuration must be a mapping")
	}
	if err := m.Retry.Validate(); err != nil {
		return err
	}
	switch m.Source {
	case "netplan":
		if m.Native != nil {
			return errors.New("native configuration requires source: native")
		}
	case "native":
		if m.Native == nil || m.NetplanPath != "" {
			return errors.New("source: native requires native configuration and no netplan_path")
		}
	default:
		return errors.New("source must be netplan or native")
	}
	return nil
}

// Load normalizes the selected source once, before bootstrap acquires resources.
func (m *Config) Load() (desired.State, error) {
	if err := m.Validate(); err != nil {
		return desired.State{}, err
	}
	if m.Source == "native" {
		return (&native.Source{Config: *m.Native}).Load()
	}
	path := m.NetplanPath
	if path == "" {
		path = "/etc/netplan/00-interfaces.yaml"
	}
	return (&netplan.Source{Path: path}).Load()
}
