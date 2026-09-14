// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package desired

// Source loads and validates immutable startup interface configuration.
type Source interface {
	Load() (State, error)
}
