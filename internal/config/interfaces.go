// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/yanet-platform/netconfig/internal/desired"
)

var decimalDigits = regexp.MustCompile(`^[0-9]+$`)

// ParseNetplan reads the managed subset, allowing unrelated host configuration.
func ParseNetplan(data []byte) (desired.State, error) {
	var root map[string]yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return desired.State{}, fmt.Errorf("decode netplan YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return desired.State{}, errors.New("decode netplan YAML: multiple documents are not supported")
		}
		return desired.State{}, fmt.Errorf("decode trailing netplan YAML: %w", err)
	}
	network, err := mapping(root["network"])
	if err != nil {
		return desired.State{}, fmt.Errorf("network: %w", err)
	}
	version := scalar(network["version"])
	if version.Kind != yaml.ScalarNode || version.Tag != "!!int" || version.Value != "2" {
		return desired.State{}, errors.New("network.version must be 2")
	}
	return parseInterfaces(network, false)
}

// Both sources normalize directly into desired state. Strict native input has
// no host sections, scalar coercion, YAML aliases or ignored routing fields.
func parseInterfaces(network map[string]yaml.Node, strict bool) (desired.State, error) {
	sections := map[string]map[string]yaml.Node{}
	for _, name := range slices.Sorted(maps.Keys(network)) {
		switch name {
		case "ethernets", "vlans", "dummy-devices":
			section, err := mapping(network[name])
			if err != nil {
				return desired.State{}, fmt.Errorf("network.%s: %w", name, err)
			}
			sections[name] = section
		case "version", "renderer", "wifis", "modems", "bridges", "bonds", "tunnels", "vrfs",
			"nm-devices", "virtual-ethernets", "openvswitch":
			if !strict {
				continue
			}
			fallthrough
		default:
			return desired.State{}, fmt.Errorf("unsupported section %q", name)
		}
	}

	state := desired.State{}
	for _, section := range []struct {
		name string
		kind desired.LinkKind
	}{
		{"ethernets", desired.LinkKindKNI}, {"vlans", desired.LinkKindVLAN}, {"dummy-devices", desired.LinkKindDummy},
	} {
		kind := section.kind
		for _, name := range slices.Sorted(maps.Keys(sections[section.name])) {
			linkKind := kind
			if kind == desired.LinkKindKNI {
				if name == "lo" {
					linkKind = desired.LinkKindLoopback
				} else if !strict && !desired.IsKNIName(name) {
					continue
				}
			}
			fields, err := mapping(sections[section.name][name])
			if err != nil {
				return desired.State{}, fmt.Errorf("link %q: %w", name, err)
			}
			parent := scalar(fields["link"])
			if kind == desired.LinkKindVLAN {
				if parent.Tag != "!!str" || parent.Value == "" {
					return desired.State{}, fmt.Errorf("vlan %q: parent link is required", name)
				}
				if _, declared := sections["ethernets"][parent.Value]; !declared || parent.Value == "lo" {
					return desired.State{}, fmt.Errorf("vlan %q: parent is not a declared Ethernet", name)
				}
				if !strict && !desired.IsKNIName(parent.Value) {
					continue
				}
			}
			link, err := parseLink(name, linkKind, fields, strict)
			if err != nil {
				return desired.State{}, err
			}
			link.Parent = parent.Value
			state.Links = append(state.Links, link)
		}
	}
	sort.Slice(state.Links, func(first, second int) bool {
		return state.Links[first].Name < state.Links[second].Name
	})
	if err := state.Validate(); err != nil {
		return desired.State{}, err
	}
	return state, nil
}

// ParseNetplanFile reads and validates the startup Netplan file.
func ParseNetplanFile(path string) (desired.State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return desired.State{}, fmt.Errorf("read netplan file %q: %w", path, err)
	}
	state, err := ParseNetplan(data)
	if err != nil {
		return desired.State{}, fmt.Errorf("parse netplan file %q: %w", path, err)
	}
	return state, nil
}

func mapping(node yaml.Node) (map[string]yaml.Node, error) {
	node = scalar(node)
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("configuration mapping is required")
	}
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func scalar(node yaml.Node) yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = *node.Alias
	}
	return node
}

func parseLink(name string, kind desired.LinkKind, fields map[string]yaml.Node, strict bool) (desired.Link, error) {
	link := desired.Link{Name: name, Kind: kind, IPv6LinkLocal: true}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		value := scalar(fields[key])
		switch key {
		case "routes", "routing-policy":
			if strict {
				return desired.Link{}, fmt.Errorf("link %q: unsupported setting %q", name, key)
			}
			continue
		case "addresses", "link-local":
			if value.Kind != yaml.SequenceNode {
				return desired.Link{}, fmt.Errorf("link %q: %s must be a sequence", name, key)
			}
			values := []string{}
			for _, item := range value.Content {
				itemValue := scalar(*item)
				if itemValue.Kind != yaml.ScalarNode || itemValue.Tag != "!!str" {
					return desired.Link{}, fmt.Errorf("link %q: %s must contain strings", name, key)
				}
				values = append(values, itemValue.Value)
			}
			if key == "link-local" {
				if strict && len(values) > 1 {
					return desired.Link{}, errors.New("link-local must be [] or [ipv6]")
				}
				for _, family := range values {
					if family != "ipv6" {
						return desired.Link{}, fmt.Errorf("link %q: unsupported link-local family %q", name, family)
					}
				}
				link.IPv6LinkLocal = len(values) != 0
				continue
			}
			for _, address := range values {
				prefix, err := netip.ParsePrefix(address)
				if err != nil {
					return desired.Link{}, fmt.Errorf("link %q: address %q: %w", name, address, err)
				}
				link.Addresses = append(link.Addresses, prefix)
			}
		case "dhcp4", "dhcp6", "accept-ra":
			var enabled bool
			if value.Kind != yaml.ScalarNode || value.Tag == "!!null" || (strict || key != "accept-ra") && value.Tag != "!!bool" {
				return desired.Link{}, fmt.Errorf("link %q: %s must be a boolean", name, key)
			}
			if err := value.Decode(&enabled); err != nil {
				return desired.Link{}, fmt.Errorf("link %q: %s: %w", name, key, err)
			}
			if key == "accept-ra" {
				link.AcceptRA = &enabled
			} else if enabled {
				return desired.Link{}, fmt.Errorf("link %q: %s must be disabled", name, key)
			}
		case "mtu":
			if value.Kind != yaml.ScalarNode || strict && value.Tag != "!!int" || !decimalDigits.MatchString(value.Value) {
				return desired.Link{}, fmt.Errorf("link %q: mtu must be a decimal integer", name)
			}
			mtu, err := strconv.ParseUint(value.Value, 10, 31)
			if err != nil {
				return desired.Link{}, fmt.Errorf("link %q: mtu: %w", name, err)
			}
			link.MTU = int(mtu)
		case "id", "link":
			if kind != desired.LinkKindVLAN {
				return desired.Link{}, fmt.Errorf("link %q: %s is only supported for VLANs", name, key)
			}
		default:
			return desired.Link{}, fmt.Errorf("link %q: unsupported setting %q", name, key)
		}
	}
	if kind == desired.LinkKindVLAN {
		value := scalar(fields["id"])
		if value.Kind != yaml.ScalarNode || value.Tag == "!!null" || strict && value.Tag != "!!int" || !decimalDigits.MatchString(value.Value) {
			return desired.Link{}, fmt.Errorf("vlan %q: id must be decimal digits in 0..4094", name)
		}
		identifier, err := strconv.ParseUint(value.Value, 10, 16)
		if err != nil || identifier > 4094 {
			return desired.Link{}, fmt.Errorf("vlan %q: id must be in 0..4094", name)
		}
		link.VLANID = int(identifier)
	}
	return link, nil
}
