package handler

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/test-infra/prow/client/clientset/versioned/typed/prowjobs/v1"
	"k8s.io/test-infra/prow/config"
	gitv2 "k8s.io/test-infra/prow/git/v2"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/labels"
	"k8s.io/test-infra/prow/pjutil"
)

var log *logrus.Logger

func init() {
	log = logrus.New()
	log.SetOutput(os.Stdout)
}

// GitHubEvent represents a valid GitHub event in the events channel
type GitHubEvent struct {
	Type    string
	GUID    string
	Payload []byte
}

type loadConfigBytesFunc func(h *GitHubEventsHandler, org, repo, branch string) ([]byte, []byte, error)

var LoadConfigBytesFunc loadConfigBytesFunc = loadConfigBytes

type GitHubEventsHandler struct {
	eventsChan       <-chan *GitHubEvent
	logger           *logrus.Logger
	prowClient       v1.ProwJobInterface
	ghClient         githubClientInterface
	gitClientFactory gitv2.ClientFactory
	prowConfigPath   string
	jobsConfigBase   string
	prowLocation     string
}

// NewGitHubEventsHandler returns a new github events handler
func NewGitHubEventsHandler(
	eventsChan <-chan *GitHubEvent,
	logger *logrus.Logger,
	prowClient v1.ProwJobInterface,
	ghClient githubClientInterface,
	prowConfigPath string,
	jobsConfigBase string,
	prowLocation string,
	gitClientFactory gitv2.ClientFactory) *GitHubEventsHandler {

	return &GitHubEventsHandler{
		eventsChan:       eventsChan,
		logger:           logger,
		prowClient:       prowClient,
		ghClient:         ghClient,
		prowConfigPath:   prowConfigPath,
		jobsConfigBase:   jobsConfigBase,
		prowLocation:     prowLocation,
		gitClientFactory: gitClientFactory,
	}
}

type githubClientInterface interface {
	GetPullRequest(string, string, int) (*github.PullRequest, error)
	CreateComment(org, repo string, number int, comment string) error
	GetPullRequestChanges(org, repo string, number int) ([]github.PullRequestChange, error)
	GetIssueLabels(org, repo string, number int) ([]github.Label, error)
}

func (h *GitHubEventsHandler) Handle(incomingEvent *GitHubEvent) {
	log.Infoln("GitHub events handler started")
	eventLog := log.WithField("event-guid", incomingEvent.GUID)
	switch incomingEvent.Type {
	case "pull_request":
		eventLog.Infoln("Handling pull request event")
		var event github.PullRequestEvent
		if err := json.Unmarshal(incomingEvent.Payload, &event); err != nil {
			eventLog.WithError(err).Error("Could not unmarshal event.")
			return
		}
		h.handlePullRequestEvent(eventLog, &event)
	default:
		log.Infoln("Dropping irrelevant:", incomingEvent.Type, incomingEvent.GUID)
	}
}

// For unit tests, as we create a local git NewFakeClient
func (h *GitHubEventsHandler) SetLocalConfLoad() {
	LoadConfigBytesFunc = loadLocalConfigBytes
}

func (h *GitHubEventsHandler) handlePullRequestEvent(log *logrus.Entry, event *github.PullRequestEvent) {
	log.Infof("Handling updated pull request: %s [%d]", event.Repo.FullName, event.PullRequest.Number)

	if !h.shouldActOnPREvent(event) {
		log.Infoln("Skipping event. Not of our interest.")
		return
	}

	org, repo, err := gitv2.OrgRepo(event.Repo.FullName)
	if err != nil {
		log.WithError(err).Errorf("Could not get org/repo from the event")
		return
	}

	shouldRun, err := h.shouldRunPhase2(org, repo, event.Label.Name, event.PullRequest.Number)
	if err != nil || !shouldRun {
		return
	}

	pr, err := h.ghClient.GetPullRequest(org, repo, event.PullRequest.Number)
	if err != nil {
		log.WithError(err).Errorf("Could not get PR number %d", event.PullRequest.Number)
		return
	}

	_, err = h.loadPresubmits(*pr)
	if err != nil {
		log.WithError(err).Errorf("loadPresubmits failed")
		return
	}
}

func (h *GitHubEventsHandler) loadPresubmits(pr github.PullRequest) ([]config.Presubmit, error) {
	tmpdir, err := ioutil.TempDir("", "prow-configs")
	if err != nil {
		log.WithError(err).Error("Could not create a temp directory to store configs.")
		return nil, err
	}
	defer os.RemoveAll(tmpdir)

	org, repo, err := gitv2.OrgRepo(pr.Base.Repo.FullName)
	if err != nil {
		log.WithError(err).Errorf("Could not parse repo name: %s", pr.Base.Repo.FullName)
		return nil, err
	}

	// TODO REMOVE
	// LoadConfigBytesFunc = loadConfigBytes
	// h.prowLocation = "https://raw.githubusercontent.com/kubevirt/project-infra/main"
	// h.prowConfigPath = "github/ci/prow-deploy/kustom/base/configs/current/config/config.yaml"
	// h.jobsConfigBase = "github/ci/prow-deploy/files/jobs"
	// org = "kubevirt"
	// repo = "kubevirt"

	var prowConfigBytes, jobConfigBytes []byte
	prowConfigBytes, jobConfigBytes, err = LoadConfigBytesFunc(h, org, repo, pr.Base.Ref)

	// if h.prowLocation == "" {
	// 	prowConfigBytes, jobConfigBytes, err = loadLocalConfigBytes(h, org, repo, pr.Base.Ref)
	// } else {
	// 	prowConfigBytes, jobConfigBytes, err = LoadConfigBytesFunc(h, org, repo, pr.Base.Ref)
	// }

	/*
		if h.prowLocation == "" {
			git, err := h.gitClientFactory.ClientFor(org, repo)
			if err != nil {
				log.WithError(err).Errorf("Could not get client for git")
				return nil, err
			}

			// TODO or unit tests, unless we create a dedicated unit tests folder(s)
			ret := 0
			// TODO think about the 128 not found
			prowConfigBytes, ret = catFile(log, git.Directory(), h.prowConfigPath, "HEAD")
			if ret != 0 && ret != 128 {
				log.WithError(err).Errorf("Could not load Prow config %s", h.prowConfigPath)
				return nil, err
			}

			// TODO maybe we can do that also on unit tests jobsConfigBase wil be the base
			// and there will be a file based on org/repo/repo-presubmit there as in real
			jobConfigBytes, ret = catFile(log, git.Directory(), h.jobsConfigBase, "HEAD")
			if ret != 0 && ret != 128 {
				log.WithError(err).Errorf("Could not load Prow config %s", h.jobsConfigBase)
				return nil, err
			}
		} else {
			prowConfigUrl := h.prowLocation + "/" + h.prowConfigPath
			prowConfigBytes, err = fetchRemoteFile(prowConfigUrl)
			if err != nil {
				log.WithError(err).Errorf("Could not fetch prow config from %s", prowConfigUrl)
				return nil, err
			}

			// branchAddon := ""
			// if strings.HasPrefix(pr.Base.Ref, "release") {
			// 	branchAddon = "-" + strings.TrimPrefix(pr.Base.Ref, "release")
			// }

			// NOTE: only branches main / master and release-<number> are supported, and must be yaml files (not yml)
			// for example kubevirt-presubmits-1.1.yaml will belong to release-1.1
			// jobConfigUrl := prowLocation + "/" + h.jobsConfigBase + "/" + org + "/" +
			// 	repo + "/" + repo + "-presubmits" + branchAddon + ".yaml"

			jobConfigUrl := generateJobConfigURL(org, repo, h.prowLocation, h.jobsConfigBase, pr.Base.Ref)
			jobConfigBytes, err = fetchRemoteFile(jobConfigUrl)
			if err != nil {
				log.WithError(err).Errorf("Could not fetch prow config from %s", jobConfigUrl)
				return nil, err
			}
		} */

	prowConfigTmp, err := writeTempFile(log, tmpdir, prowConfigBytes)
	if err != nil {
		log.WithError(err).Errorf("Could not write temporary Prow config.")
		return nil, err
	}

	jobConfigTmp, err := writeTempFile(log, tmpdir, jobConfigBytes)
	if err != nil {
		log.WithError(err).Infoln("Could not write temp file")
		return nil, err
	}

	pc, err := config.Load(prowConfigTmp, jobConfigTmp, nil, "")
	if err != nil {
		log.WithError(err).Errorf("Could not load prow config")
		return nil, err
	}

	orgRepo := org + "/" + repo
	var presumbits []config.Presubmit
	for index, jobs := range pc.PresubmitsStatic {
		if index != orgRepo {
			continue
		}
		for _, job := range jobs {
			presumbits = append(presumbits, job)
			// TODO REMOVE
			log.Infof("DBG presubmit %s", job.Name)
		}
	}

	// TODO move
	err = listRequiredManual(h.ghClient, pr, presumbits)

	return presumbits, nil
}

func generateJobConfigURL(org, repo, prowLocation, jobsConfigBase string, branch string) string {
	// Only branches main / master and release-<number> are supported
	// for example kubevirt-presubmits-1.1.yaml will belong to release-1.1
	// and kubevirt-presubmits.yaml belong to main / master
	// must be yaml files (not yml)

	branchAddon := ""
	if strings.HasPrefix(branch, "release") {
		branchAddon = "-" + strings.TrimPrefix(branch, "release")
	}

	return fmt.Sprintf("%s/%s/%s/%s/%s-presubmits%s.yaml",
		prowLocation, jobsConfigBase, org, repo, repo, branchAddon)
}

func (h *GitHubEventsHandler) shouldActOnPREvent(event *github.PullRequestEvent) bool {
	return event.Action == github.PullRequestActionLabeled
}

func (h *GitHubEventsHandler) shouldRunPhase2(org, repo, eventLabel string, prNum int) (bool, error) {
	l, err := h.ghClient.GetIssueLabels(org, repo, prNum)
	if err != nil {
		log.WithError(err).Errorf("Could not get PR labels")
		return false, err
	}

	return (eventLabel == labels.LGTM && github.HasLabel(labels.Approved, l)) ||
		(eventLabel == labels.Approved && github.HasLabel(labels.LGTM, l)), nil
}

func catFile(log *logrus.Logger, gitDir, file, refspec string) ([]byte, int) {
	cmd := exec.Command("git", "-C", gitDir, "cat-file", "-p", fmt.Sprintf("%s:%s", refspec, file))
	log.Debugf("Executing git command: %+v", cmd.Args)
	out, _ := cmd.CombinedOutput()
	return out, cmd.ProcessState.ExitCode()
}

func writeTempFile(log *logrus.Logger, basedir string, content []byte) (string, error) {
	tmpfile, err := ioutil.TempFile(basedir, "job-config")
	if err != nil {
		log.WithError(err).Errorf("Could not create temp file for job config.")
		return "", err
	}
	defer tmpfile.Close()
	_, err = tmpfile.Write(content)
	if err != nil {
		log.WithError(err).Errorf("Could not write data to file: %s", tmpfile.Name())
		return "", err
	}
	tmpfile.Sync()
	return tmpfile.Name(), nil
}

func fetchRemoteFile(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP request failed with status: %v", resp.Status)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func listRequiredManual(ghClient githubClientInterface, pr github.PullRequest, presubmits []config.Presubmit) error {
	if pr.Draft || pr.Merged || pr.State != "open" {
		return nil
	}

	if pr.Mergable != nil && !*pr.Mergable {
		return nil
	}

	org, repo, number, branch := pr.Base.Repo.Owner.Login, pr.Base.Repo.Name, pr.Number, pr.Base.Ref
	changes := config.NewGitHubDeferredChangedFilesProvider(ghClient, org, repo, number)
	toTest, err := pjutil.FilterPresubmits(manualRequiredFilter, changes, branch, presubmits, log)
	if err != nil {
		return err
	}

	return listRequested(ghClient, pr, toTest)
}

func manualRequiredFilter(p config.Presubmit) (bool, bool, bool) {
	cond := !p.Optional && !p.AlwaysRun && p.RegexpChangeMatcher.RunIfChanged == "" &&
		p.RegexpChangeMatcher.SkipIfOnlyChanged == ""
	return cond, cond, false
}

func listRequested(ghClient githubClientInterface, pr github.PullRequest, requestedJobs []config.Presubmit) error {
	org, repo, err := gitv2.OrgRepo(pr.Base.Repo.FullName)
	if err != nil {
		log.WithError(err).Errorf("Could not parse repo name: %s", pr.Base.Repo.FullName)
		return err
	}

	// Note: instead reading config and allowing only "require_manually_triggered_jobs" repos
	if !(org == "kubevirt" && repo == "kubevirt") && !(org == "foo" && repo == "bar") {
		return nil
	}

	var result string
	for _, job := range requestedJobs {
		result += "/test " + job.Name + "\n"
		// REMOVE
		//log.Infof("DBG presubmit %s", job.Name)
	}

	if result != "" {
		// REMOVE
		//log.Infof("DBG presubmit %s %s %d %s", org, repo, pr.Number, result)
		if err := ghClient.CreateComment(org, repo, pr.Number, result); err != nil {
			return err
		}
	}

	return nil
}

func loadLocalConfigBytes(h *GitHubEventsHandler, org, repo, branch string) ([]byte, []byte, error) {
	git, err := h.gitClientFactory.ClientFor(org, repo)
	if err != nil {
		log.WithError(err).Errorf("Could not get client for git")
		return nil, nil, err
	}

	prowConfigBytes, ret := catFile(log, git.Directory(), h.prowConfigPath, "HEAD")
	if ret != 0 {
		log.WithError(err).Errorf("Could not load Prow config %s", h.prowConfigPath)
		return nil, nil, err
	}

	jobConfigBytes, ret := catFile(log, git.Directory(), h.jobsConfigBase, "HEAD")
	if ret != 0 {
		log.WithError(err).Errorf("Could not load Prow config %s", h.jobsConfigBase)
		return nil, nil, err
	}

	return prowConfigBytes, jobConfigBytes, nil
}

func loadConfigBytes(h *GitHubEventsHandler, org, repo, branch string) ([]byte, []byte, error) {
	prowConfigUrl := h.prowLocation + "/" + h.prowConfigPath
	prowConfigBytes, err := fetchRemoteFile(prowConfigUrl)
	if err != nil {
		log.WithError(err).Errorf("Could not fetch prow config from %s", prowConfigUrl)
		return nil, nil, err
	}

	jobConfigUrl := generateJobConfigURL(org, repo, h.prowLocation, h.jobsConfigBase, branch)
	jobConfigBytes, err := fetchRemoteFile(jobConfigUrl)
	if err != nil {
		log.WithError(err).Errorf("Could not fetch prow config from %s", jobConfigUrl)
		return nil, nil, err
	}

	return prowConfigBytes, jobConfigBytes, nil
}
