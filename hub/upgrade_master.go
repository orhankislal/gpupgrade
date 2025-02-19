// Copyright (c) 2017-2021 VMware, Inc. or its affiliates
// SPDX-License-Identifier: Apache-2.0

package hub

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/xerrors"

	"github.com/greenplum-db/gpupgrade/greenplum"
	"github.com/greenplum-db/gpupgrade/step"
	"github.com/greenplum-db/gpupgrade/upgrade"
	"github.com/greenplum-db/gpupgrade/utils"
	"github.com/greenplum-db/gpupgrade/utils/errorlist"
	"github.com/greenplum-db/gpupgrade/utils/rsync"
)

// Allow exec.Command to be mocked out by exectest.NewCommand.
var cmd = exec.Command

const originalMasterBackupName = "master.bak"

type UpgradeMasterArgs struct {
	Source       *greenplum.Cluster
	Intermediate *greenplum.Cluster
	StateDir     string
	Stream       step.OutStreams
	CheckOnly    bool
	UseLinkMode  bool
}

func UpgradeMaster(args UpgradeMasterArgs) error {
	wd, err := utils.GetPgUpgradeDir(greenplum.PrimaryRole, -1)
	if err != nil {
		return err
	}

	err = utils.System.MkdirAll(wd, 0700)
	if err != nil {
		return err
	}

	sourceDir := filepath.Join(args.StateDir, originalMasterBackupName)
	err = RsyncMasterDataDir(args.Stream, sourceDir, args.Intermediate.MasterDataDir())
	if err != nil {
		return err
	}

	pair := upgrade.SegmentPair{
		Source: masterSegmentFromCluster(args.Source),
		Target: masterSegmentFromCluster(args.Intermediate),
	}

	// Buffer stdout to add context to errors.
	stdout := new(bytes.Buffer)
	tee := io.MultiWriter(args.Stream.Stdout(), stdout)

	options := []upgrade.Option{
		upgrade.WithExecCommand(cmd),
		upgrade.WithWorkDir(wd),
		upgrade.WithOutputStreams(tee, args.Stream.Stderr()),
	}

	if args.CheckOnly {
		options = append(options, upgrade.WithCheckOnly())
	}

	if args.UseLinkMode {
		options = append(options, upgrade.WithLinkMode())
	}

	// When upgrading from 5 the master must be provided with its standby's dbid to allow WAL to sync.
	if args.Source.Version.Major == 5 {
		if args.Source.HasStandby() {
			options = append(options, upgrade.WithOldOptions(fmt.Sprintf("-x %d", args.Source.Standby().DbID)))
		}
	}

	err = upgrade.Run(pair, args.Intermediate.Version, options...)
	if err != nil {
		// Error details from stdout are added to any errors containing "fatal"
		// such as pg_ugprade check errors.
		var text []string
		var addText bool

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()

			// XXX Checking for any instance of "fatal" is overly broad, but it
			// keeps us from coupling against pg_upgrade UI specifics, which are
			// currently evolving. We are guaranteed not to print too little
			// information, though we may print too much on a spurious match.
			// Revisit when the UI settles.
			if strings.Contains(line, "fatal") || addText {
				addText = true
				text = append(text, line)
			}
		}
		errText := strings.Join(text, "\n")

		// include the full path of any pg_upgrade error files
		errorFiles, dirErr := fileEntries(wd)
		err = errorlist.Append(err, dirErr)
		for _, errFile := range errorFiles {
			errText = strings.ReplaceAll(errText, errFile, filepath.Join(wd, errFile))
		}

		if args.CheckOnly {
			nextAction := `Ensure the "pre-initialize" data migration scripts have been run. 
Consult the gpupgrade documentation for details on the pg_upgrade check error.`
			return utils.NewNextActionErr(NewUpgradeMasterError(args.CheckOnly, errText, err), nextAction)
		}

		return NewUpgradeMasterError(args.CheckOnly, errText, err)
	}

	return nil
}

type UpgradeMasterError struct {
	FailedAction string
	ErrorText    string
	err          error
}

func NewUpgradeMasterError(checkOnly bool, errText string, err error) UpgradeMasterError {
	failedAction := "upgrade"
	if checkOnly {
		failedAction = "check"
	}

	return UpgradeMasterError{
		FailedAction: failedAction,
		ErrorText:    errText,
		err:          err,
	}
}

func (u UpgradeMasterError) Error() string {
	if u.ErrorText == "" {
		return fmt.Sprintf("%s master: %v", u.FailedAction, u.err)
	}

	return fmt.Sprintf("%s master: %s: %v", u.FailedAction, u.ErrorText, u.err)
}

func (u UpgradeMasterError) Unwrap() error {
	return u.err
}

func masterSegmentFromCluster(cluster *greenplum.Cluster) *upgrade.Segment {
	return &upgrade.Segment{
		BinDir:  filepath.Join(cluster.GPHome, "bin"),
		DataDir: cluster.MasterDataDir(),
		DBID:    cluster.Master().DbID,
		Port:    cluster.MasterPort(),
	}
}

// fileEntries returns a list of all filenames
//   under the given root.
func fileEntries(root string) ([]string, error) {
	entries, err := ioutil.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		files = append(files, entry.Name())
	}

	return files, nil
}

func RsyncMasterDataDir(stream step.OutStreams, sourceDir, targetDir string) error {
	sourceDirRsync := filepath.Clean(sourceDir) + string(os.PathSeparator)

	options := []rsync.Option{
		rsync.WithSources(sourceDirRsync),
		rsync.WithDestination(targetDir),
		rsync.WithOptions("--archive", "--delete"),
		rsync.WithExcludedFiles("pg_log/*"),
		rsync.WithStream(stream),
	}

	err := rsync.Rsync(options...)
	if err != nil {
		return xerrors.Errorf("rsync %q to %q: %w", sourceDirRsync, targetDir, err)
	}

	return nil
}
