package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/GrigoryKrasnochub/updaterini"
	"github.com/anywherelan/awl/config"
	"github.com/ipfs/go-log/v2"
)

// updChannels oder is priority, release chan is always in chan-s list and has max priority
const (
	gitUserName = "anywherelan"
	gitRepoName = "awl"
)

var updChannels = []string{
	"rc",
}

type UpdateService struct {
	updConf    updaterini.UpdateConfig
	NewVersion updaterini.Version
	logger     *log.ZapEventLogger
}

type ApplicationType int

const (
	AppTypeAwl ApplicationType = iota
	AppTypeAwlTray
)

var (
	awlFilenamesRegex     = regexp.MustCompile(fmt.Sprintf(".*_awl_%s_%s.*", runtime.GOOS, runtime.GOARCH)) // TODO sync with build scripts
	awlTrayFilenamesRegex = regexp.MustCompile(fmt.Sprintf(".*_awl-tray_%s_%s.*", runtime.GOOS, runtime.GOARCH))
)

func NewUpdateService(c *config.Config, logger *log.ZapEventLogger, appType ApplicationType) (UpdateService, error) {
	channels := make([]updaterini.Channel, 1)
	channels[0] = updaterini.NewReleaseChannel(true)

	// setup all channels
	lowestInChan := c.Update.LowestPriorityChan == ""
	for _, ch := range updChannels {
		channels = append(channels, updaterini.NewChannel(ch, !lowestInChan))
		if ch == c.Update.LowestPriorityChan {
			lowestInChan = true
		}
	}

	// if lowest is custom
	if !lowestInChan {
		channels = append(channels, updaterini.NewChannel(c.Update.LowestPriorityChan, true))
	}

	filenamesRegex := make([]*regexp.Regexp, 1, 1)
	switch appType {
	case AppTypeAwl:
		filenamesRegex[0] = awlFilenamesRegex
	case AppTypeAwlTray:
		filenamesRegex[0] = awlTrayFilenamesRegex
	}

	appConf, err := updaterini.NewApplicationConfig(config.Version, channels, filenamesRegex)
	if err != nil {
		return UpdateService{}, err
	}
	appConf.ShowPrepareVersionErr = true
	return UpdateService{
		updConf: updaterini.UpdateConfig{
			ApplicationConfig: appConf,
			Sources: []updaterini.UpdateSource{
				&updaterini.UpdateSourceServer{
					UpdatesMapURL: c.Update.UpdateServerURL,
				},
				&updaterini.UpdateSourceGitRepo{
					UserName:                 gitUserName,
					RepoName:                 gitRepoName,
					SkipGitReleaseDraftCheck: false,
					PersonalAccessToken:      "",
				},
			},
		},
		NewVersion: nil,
		logger:     logger,
	}, err
}

func (uc *UpdateService) CheckForUpdates() (bool, error) {
	var srcCheckStatus updaterini.SourceCheckStatus
	uc.NewVersion, srcCheckStatus = uc.updConf.CheckForUpdates()
	status := uc.NewVersion != nil
	if srcCheckStatus.Status == updaterini.CheckSuccess {
		return status, nil
	}
	for _, srcStatus := range srcCheckStatus.SourcesStatuses {
		switch srcStatus.Status {
		case updaterini.CheckSuccess:
			continue
		case updaterini.CheckHasErrors:
			for _, err := range srcStatus.Errors {
				uc.logger.Warnf("update: check sources: source: %s: %v", srcStatus.Source.SourceLabel(), err)
			}
		case updaterini.CheckFailure:
			for _, err := range srcStatus.Errors {
				uc.logger.Errorf("update: check sources: source: %s: %v", srcStatus.Source.SourceLabel(), err)
			}
		}
	}
	if srcCheckStatus.Status == updaterini.CheckFailure {
		return false, errors.New("update failed")
	}
	return status, nil
}

func (uc *UpdateService) DoUpdate() (updaterini.UpdateResult, error) {
	curFile, err := os.Executable()
	if err != nil {
		return updaterini.UpdateResult{}, err
	}
	curFile = filepath.Base(curFile)

	return uc.updConf.DoUpdate(uc.NewVersion, "", func(loadedFilename string) (updaterini.ReplacementFile, error) {
		return updaterini.ReplacementFile{
			FileName: curFile,
			Mode:     updaterini.ReplacementFileInfoUseDefaultOrExistedFilePerm,
		}, nil
	}, func() error {
		return nil
	})
}
