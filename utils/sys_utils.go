// Copyright (c) 2017-2021 VMware, Inc. or its affiliates
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/google/renameio"

	"github.com/greenplum-db/gpupgrade/utils/errorlist"
)

var (
	System = InitializeSystemFunctions()
)

/*
 * SystemFunctions holds function pointers for built-in functions that will need
 * to be mocked out for unit testing.  All built-in functions manipulating the
 * filesystem, shell, or environment should ideally be called through a function
 * pointer in System (the global SystemFunctions variable) instead of being called
 * directly.
 */

type SystemFunctions struct {
	Current      func() (*user.User, error)
	Getenv       func(key string) string
	Getpid       func() int
	Hostname     func() (string, error)
	IsNotExist   func(err error) bool
	LookupIP     func(host string) ([]net.IP, error)
	MkdirAll     func(path string, perm os.FileMode) error
	Now          func() time.Time
	Open         func(name string) (*os.File, error)
	OpenFile     func(name string, flag int, perm os.FileMode) (*os.File, error)
	Remove       func(name string) error
	RemoveAll    func(name string) error
	Rename       func(oldpath, newpath string) error
	ReadFile     func(filename string) ([]byte, error)
	WriteFile    func(filename string, data []byte, perm os.FileMode) error
	Stat         func(name string) (os.FileInfo, error)
	FilePathGlob func(pattern string) ([]string, error)
	Create       func(name string) (*os.File, error)
	Mkdir        func(name string, perm os.FileMode) error
	Symlink      func(oldname, newname string) error
	Lstat        func(name string) (os.FileInfo, error)
}

func InitializeSystemFunctions() *SystemFunctions {
	return &SystemFunctions{
		Current:      user.Current,
		Getenv:       os.Getenv,
		Getpid:       os.Getpid,
		Hostname:     os.Hostname,
		IsNotExist:   os.IsNotExist,
		LookupIP:     net.LookupIP,
		MkdirAll:     os.MkdirAll,
		Now:          time.Now,
		Open:         os.Open,
		OpenFile:     os.OpenFile,
		Remove:       os.Remove,
		RemoveAll:    os.RemoveAll,
		Rename:       os.Rename,
		Stat:         os.Stat,
		FilePathGlob: filepath.Glob,
		ReadFile:     ioutil.ReadFile,
		WriteFile:    ioutil.WriteFile,
		Create:       os.Create,
		Mkdir:        os.Mkdir,
		Symlink:      os.Symlink,
		Lstat:        os.Lstat,
	}
}

func GetStateDir() string {
	stateDir := os.Getenv("GPUPGRADE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(os.Getenv("HOME"), ".gpupgrade")
	}

	return stateDir
}

func GetLogDir() (string, error) {
	currentUser, err := System.Current()
	if err != nil {
		return "", err
	}

	logDir := filepath.Join(currentUser.HomeDir, "gpAdminLogs", "gpupgrade")
	return logDir, nil
}

func GetTablespaceDir() string {
	return filepath.Join(GetStateDir(), "tablespaces")
}

func GetInitsystemConfig() string {
	return filepath.Join(GetStateDir(), "gpinitsystem_config")
}

func GetPgUpgradeDir(role string, contentID int) (string, error) {
	logDir, err := GetLogDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(logDir, "pg_upgrade", role + strconv.Itoa(contentID)), nil
}

// GetTablespaceMappingFile returns the tablespace input file for pg_upgrade used
// to upgrade tablespaces.
func GetTablespaceMappingFile() string {
	return filepath.Join(GetTablespaceDir(), "tablespaces.txt")
}

func GetAddMirrorsConfig() string {
	return filepath.Join(GetStateDir(), "add_mirrors_config")
}

// Returns path to a JSON file, and if one does not exist it creates an empty
// JSON file.
func GetJSONFile(stateDir string, fileName string) (path string, err error) {
	path = filepath.Join(stateDir, fileName)

	f, err := os.OpenFile(path, os.O_EXCL|os.O_CREATE|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return path, nil
	}
	if err != nil {
		return "", err
	}

	defer func() {
		if cErr := f.Close(); cErr != nil {
			err = errorlist.Append(err, cErr)
		}
	}()

	// MarshallJSON requires a well-formed JSON file
	_, err = f.WriteString("{}")
	if err != nil {
		return "", err
	}

	return path, nil
}

func GetGpupgradePath() (string, error) {
	hubPath, err := os.Executable()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(hubPath), "gpupgrade"), nil
}

// Calling os.Rename for a directory is allowed only when both the
// source and the destination path are on the top layer of filesystem.
// Otherwise, it returns EXDEV error ("cross-device link not permitted").
// To avoid such case, use the Move utility instead of os.Rename.
// Found this issue on docker containers, when os.Rename was being used
// to archive the gpupgrade log directory.
func Move(src string, dst string) error {
	cmd := exec.Command("mv", src, dst)
	_, err := cmd.Output()

	return err
}

func AtomicallyWrite(path string, data []byte) (err error) {
	// Use renameio to atomically write the file located at path.
	var file *renameio.PendingFile
	file, err = renameio.TempFile("", path)
	if err != nil {
		return err
	}
	defer func() {
		if cErr := file.Cleanup(); cErr != nil {
			err = errorlist.Append(err, cErr)
		}
	}()

	_, err = file.Write(data)
	if err != nil {
		return err
	}

	return file.CloseAtomicallyReplace()
}

// Sanitize sorts and deduplicates a slice of ints.
func Sanitize(ports []int) []int {
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })

	dedupe := ports[:0] // point at the same backing array

	var last int
	for i, port := range ports {
		if i == 0 || port != last {
			dedupe = append(dedupe, port)
		}
		last = port
	}

	return dedupe
}

// FilterEnv selects only the specified variables from the environment and
// returns those key/value pairs, in the key=value format expected by
// os/exec.Cmd.Env.
func FilterEnv(keys []string) []string {
	var env []string

	for _, key := range keys {
		val, ok := os.LookupEnv(key)
		if !ok {
			continue
		}

		env = append(env, fmt.Sprintf("%s=%s", key, val))
	}

	return env
}
