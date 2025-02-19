// Copyright (c) 2017-2021 VMware, Inc. or its affiliates
// SPDX-License-Identifier: Apache-2.0

package step_test

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/greenplum-db/gpupgrade/idl"
	"github.com/greenplum-db/gpupgrade/step"
	"github.com/greenplum-db/gpupgrade/testutils"
)

func TestFileStore(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Errorf("removing temp directory: %v", err)
		}
	}()

	path := filepath.Join(tmpDir, step.SubstepsFileName)
	fs := step.NewSubstepStoreUsingFile(path)

	const initialize = idl.Step_INITIALIZE

	t.Run("ReadStep returns errors when failing to read", func(t *testing.T) {
		_, err := fs.ReadStep(initialize)
		if !os.IsNotExist(err) {
			t.Errorf("returned error %#v, want ErrNotExist", err)
		}
	})

	t.Run("Read returns errors when failing to read", func(t *testing.T) {
		_, err := fs.Read(initialize, idl.Substep_CHECK_UPGRADE)
		if !os.IsNotExist(err) {
			t.Errorf("returned error %#v, want ErrNotExist", err)
		}
	})

	t.Run("ReadStep reads the same status that was written", func(t *testing.T) {
		clear(t, path)

		substep := idl.Substep_CHECK_UPGRADE
		status := idl.Status_COMPLETE

		err := fs.Write(initialize, substep, status)
		if err != nil {
			t.Fatalf("Write() returned error %#v", err)
		}

		statusMap, err := fs.ReadStep(initialize)
		if err != nil {
			t.Errorf("Read() returned error %#v", err)
		}

		expected := step.PrettyStatus{Status: status}
		if !reflect.DeepEqual(statusMap[substep.String()], expected) {
			t.Errorf("read %v, want %v", statusMap, expected)
		}
	})

	t.Run("reads the same status that was written", func(t *testing.T) {
		clear(t, path)

		substep := idl.Substep_CHECK_UPGRADE
		expected := idl.Status_COMPLETE

		err := fs.Write(initialize, substep, expected)
		if err != nil {
			t.Fatalf("Write() returned error %#v", err)
		}

		status, err := fs.Read(initialize, substep)
		if err != nil {
			t.Errorf("Read() returned error %#v", err)
		}

		if status != expected {
			t.Errorf("read %v, want %v", status, expected)
		}
	})

	t.Run("can write to the same substep in different sections", func(t *testing.T) {
		clear(t, path)

		substep := idl.Substep_CHECK_UPGRADE
		entries := []struct {
			Section idl.Step
			Status  idl.Status
		}{
			{Section: idl.Step_INITIALIZE, Status: idl.Status_FAILED},
			{Section: idl.Step_EXECUTE, Status: idl.Status_COMPLETE},
		}

		for _, e := range entries {
			err := fs.Write(e.Section, substep, e.Status)
			if err != nil {
				t.Fatalf("Write(%q, %v, %v) returned error %+v",
					e.Section, substep, e.Status, err)
			}
		}

		for _, e := range entries {
			status, err := fs.Read(e.Section, substep)
			if err != nil {
				t.Errorf("Read(%q, %v) returned error %#v", e.Section, substep, err)
			}
			if status != e.Status {
				t.Errorf("Read(%q, %v) = %v, want %v", e.Section, substep,
					status, e.Status)
			}
		}
	})

	t.Run("ReadStep returns nil if requested step has not been written", func(t *testing.T) {
		clear(t, path)

		status, err := fs.ReadStep(initialize)
		if err != nil {
			t.Errorf("Read() returned error %#v", err)
		}

		if status != nil {
			t.Errorf("read %v, want nil", status)
		}
	})

	t.Run("returns unknown status if requested step has not been written", func(t *testing.T) {
		clear(t, path)

		status, err := fs.Read(initialize, idl.Substep_INIT_TARGET_CLUSTER)
		if err != nil {
			t.Errorf("Read() returned error %#v", err)
		}

		expected := idl.Status_UNKNOWN_STATUS
		if status != expected {
			t.Errorf("read %v, want %v", status, expected)
		}
	})

	t.Run("returns unknown status if substep was not written to the requested step", func(t *testing.T) {
		clear(t, path)

		err := fs.Write(initialize, idl.Substep_CHECK_UPGRADE, idl.Status_FAILED)
		if err != nil {
			t.Fatalf("Write() returned error %+v", err)
		}

		status, err := fs.Read(initialize, idl.Substep_INIT_TARGET_CLUSTER)
		if err != nil {
			t.Errorf("Read() returned error %#v", err)
		}

		expected := idl.Status_UNKNOWN_STATUS
		if status != expected {
			t.Errorf("read %v, want %v", status, expected)
		}
	})

	t.Run("returns unknown status if substep was written to a different step", func(t *testing.T) {
		clear(t, path)

		err := fs.Write(idl.Step_FINALIZE, idl.Substep_INIT_TARGET_CLUSTER, idl.Status_FAILED)
		if err != nil {
			t.Fatalf("Write() returned error %+v", err)
		}

		status, err := fs.Read(initialize, idl.Substep_INIT_TARGET_CLUSTER)
		if err != nil {
			t.Errorf("Read() returned error %#v", err)
		}

		expected := idl.Status_UNKNOWN_STATUS
		if status != expected {
			t.Errorf("read %v, want %v", status, expected)
		}
	})

	t.Run("uses human-readable serialization", func(t *testing.T) {
		substep := idl.Substep_INIT_TARGET_CLUSTER
		status := idl.Status_FAILED
		if err := fs.Write(initialize, substep, status); err != nil {
			t.Fatalf("Write(): %+v", err)
		}

		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("opening file: %+v", err)
		}
		defer f.Close()

		dec := json.NewDecoder(f)
		raw := make(map[string]map[string]string)
		if err := dec.Decode(&raw); err != nil {
			t.Fatalf("decoding statuses: %+v", err)
		}

		key := substep.String()
		if raw[initialize.String()][key] != status.String() {
			t.Errorf("status[%q][%q] = %q, want %q", initialize, key, raw[initialize.String()][key], status.String())
		}
	})
}

// clear writes an empty JSON map to the given SubstepFileStore backing path.
func clear(t *testing.T, path string) {
	t.Helper()

	testutils.MustWriteToFile(t, path, "{}")
}
