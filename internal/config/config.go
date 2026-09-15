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

	"go.yaml.in/yaml/v3"

	"github.com/yanet-platform/netconfig/internal/bootstrap"
	"github.com/yanet-platform/netconfig/internal/desired"
)

// Config selects exactly one interface source and the bootstrap retry policy.
type Config struct {
	Source      string           `yaml:"source"`
	NetplanPath string           `yaml:"netplan_path"`
	Native      yaml.Node        `yaml:"native"`
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
	if config == nil || config.Source != "native" && config.Source != "netplan" {
		return nil, errors.New("source must be netplan or native")
	}
	if config.Source == "native" && config.Native.Kind != yaml.MappingNode {
		return nil, errors.New("source: native requires native configuration")
	}
	if config.Retry.InitialBackoff <= 0 || config.Retry.MaxBackoff < config.Retry.InitialBackoff {
		return nil, errors.New("retry requires 0 < initial_backoff <= max_backoff")
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

// Load normalizes the selected source once, before bootstrap acquires resources.
func (m *Config) Load() (desired.State, error) {
	if m.Source == "native" {
		if err := validateNative(&m.Native); err != nil {
			return desired.State{}, err
		}
		fields, err := mapping(m.Native)
		if err != nil {
			return desired.State{}, err
		}
		return parseInterfaces(fields, true)
	}
	path := m.NetplanPath
	if path == "" {
		path = "/etc/netplan/00-interfaces.yaml"
	}
	return ParseNetplanFile(path)
}

// Reject aliases, merge keys and non-string keys before map decoding can coerce
// them. Duplicate keys are rejected by the YAML decoder at each mapping level.
func validateNative(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode && node.Tag != "!!map" || node.Kind == yaml.SequenceNode && node.Tag != "!!seq" {
		return errors.New("native collections must use standard YAML types")
	}
	if node.Kind == yaml.AliasNode {
		return errors.New("native YAML aliases are not supported")
	}
	for i, child := range node.Content {
		if node.Kind == yaml.MappingNode && i%2 == 0 && child.Tag != "!!str" {
			return errors.New("native mapping keys must be strings")
		}
		if err := validateNative(child); err != nil {
			return err
		}
	}
	return nil
}
