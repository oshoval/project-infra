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
	"k8s.io/test-infra/prow/pjutil"
)

// const basicHelpCommentText = `You can trigger rehearsal for all jobs by commenting either ` + "`/rehearse`" + ` or ` + "`/rehearse all`" + `
// on this PR.

// For a specific PR you can comment ` + "`/rehearse {job-name}`" + `.

// For a list of jobs that you can rehearse you can comment ` + "`/rehearse ?`" + `.`

var log *logrus.Logger

// // rehearseCommentRe matches either the sole command, i.e.
// // /rehearse
// // or the command followed by a job name which we then extract by the
// // capturing group, i.e.
// // /rehearse job-name
// var rehearseCommentRe = regexp.MustCompile(`(?m)^/rehearse\s*?($|\s.*)`)

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
	IsMember(string, string) (bool, error)
	GetPullRequest(string, string, int) (*github.PullRequest, error)
	CreateComment(org, repo string, number int, comment string) error
	GetPullRequestChanges(org, repo string, number int) ([]github.PullRequestChange, error)
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
		h.handlePullRequestUpdateEvent(eventLog, &event)
	default:
		log.Infoln("Dropping irrelevant:", incomingEvent.Type, incomingEvent.GUID)
	}
}

func (h *GitHubEventsHandler) shouldActOnPREvent(event *github.PullRequestEvent) bool {
	switch event.Action {
	case github.PullRequestActionLabeled:
		return true
	default:
		return false
	}
}

func (h *GitHubEventsHandler) handlePullRequestUpdateEvent(log *logrus.Entry, event *github.PullRequestEvent) {
	// TODO remove
	// defer func() {
	// 	if r := recover(); r != nil {
	// 		h.logger.Warnf("Recovered during handling of a pull request event: %s", event.GUID)
	// 	}
	// }()

	log.Infof("Handling updated pull request: %s [%d]", event.Repo.FullName, event.PullRequest.Number)

	if !h.shouldActOnPREvent(event) {
		log.Infoln("Skipping event. Not of our interest.")
		return
	}

	// TODO maybe needed for additional fields ?
	// pr, err := h.ghClient.GetPullRequest(org, repo, event.PullRequest.Number)
	// if err != nil {
	// 	log.WithError(err).Errorf("Could not get PR number %d", event.PullRequest.Number)
	// }

	// TODO add the logic of labels

	pr := event.PullRequest
	org, repo, err := gitv2.OrgRepo(pr.Base.Repo.FullName)
	if err != nil {
		log.WithError(err).Errorf("Could not parse repo name: %s", pr.Base.Repo.FullName)
		return
	}
	log.Infoln("Generating git client")
	git, err := h.gitClientFactory.ClientFor(org, repo)
	if err != nil {
		return
	}

	// TODO move to other func
	_, err = h.loadPresubmits(git, pr)
	if err != nil {
		//log.WithError(err).Errorf("Could not parse repo name: %s", pr.Base.Repo.FullName)
		return
	}
}

func (h *GitHubEventsHandler) loadPresubmits(git gitv2.RepoClient, pr github.PullRequest) ([]config.Presubmit, error) {
	tmpdir, err := ioutil.TempDir("", "prow-configs")
	if err != nil {
		log.WithError(err).Error("Could not create a temp directory to store configs.")
		return nil, err
	}
	defer os.RemoveAll(tmpdir)

	// TODO REMOVE
	// h.prowLocation = "https://raw.githubusercontent.com/kubevirt/project-infra/main"
	// h.prowConfigPath = "github/ci/prow-deploy/kustom/base/configs/current/config/config.yaml"
	// h.jobsConfigBase = "github/ci/prow-deploy/files/jobs"

	var prowConfigBytes, jobConfigBytes []byte
	prowLocation := h.prowLocation
	if prowLocation == "" {
		// TODO or unit tests, unless we create a dedicated unit tests folder(s)
		prowLocation = git.Directory()
		ret := 0
		prowConfigBytes, ret = catFile(log, prowLocation, h.prowConfigPath, "HEAD")
		if ret != 0 && ret != 128 {
			log.WithError(err).Errorf("Could not load Prow config %s", h.prowConfigPath)
			return nil, err
		}

		jobConfigBytes, ret = catFile(log, prowLocation, h.jobsConfigBase, "HEAD")
		if ret != 0 && ret != 128 {
			log.WithError(err).Errorf("Could not load Prow config %s", h.jobsConfigBase)
			return nil, err
		}
	} else {
		prowConfigUrl := prowLocation + "/" + h.prowConfigPath
		prowConfigBytes, err = fetchRemoteFile(prowConfigUrl)
		if err != nil {
			log.WithError(err).Errorf("Could not fetch prow config from %s", prowConfigUrl)
			return nil, err
		}

		org, repo, err := gitv2.OrgRepo(pr.Base.Repo.FullName)

		// TODO REMOVE
		// org = "kubevirt"
		// repo = "kubevirt"

		if err != nil {
			log.WithError(err).Errorf("Could not parse repo name: %s", pr.Base.Repo.FullName)
			return nil, err
		}

		branchAddon := ""
		if strings.HasPrefix(pr.Base.Ref, "release") {
			branchAddon = "-" + strings.TrimPrefix(pr.Base.Ref, "release")
		}

		// NOTE: only branches main / master and release-<number> are supported, and must be yaml files (not yml)
		// for example kubevirt-presubmits-1.1.yaml will belong to release-1.1
		jobConfigUrl := prowLocation + "/" + h.jobsConfigBase + "/" + org + "/" +
			repo + "/" + repo + "-presubmits" + branchAddon + ".yaml"
		jobConfigBytes, err = fetchRemoteFile(jobConfigUrl)
		if err != nil {
			log.WithError(err).Errorf("Could not fetch prow config from %s", jobConfigUrl)
			return nil, err
		}
	}

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

	var presumbits []config.Presubmit
	// TODO maybe need to filter about key = org/repo (kubevirt/kubevirt) because prow config might have stuff of other branches
	for _, jobs := range pc.PresubmitsStatic {
		for _, job := range jobs {
			presumbits = append(presumbits, job)
			// TODO REMOVE
			//log.Infof("DBG presubmit %s, %s", job.Name)
		}
	}

	//panic("bla")
	// REMOVE
	err = listRequiredManual(h.ghClient, pr, presumbits)

	return presumbits, nil
}

/*
func (h *GitHubEventsHandler) loadConfigsAtRef(git gitv2.RepoClient, pr *github.PullRequest) (map[string]*config.Config, error) {
	configs := map[string]*config.Config{}

	tmpdir, err := ioutil.TempDir("", "prow-configs")
	if err != nil {
		log.WithError(err).Error("Could not create a temp directory to store configs.")
		return nil, err
	}
	defer os.RemoveAll(tmpdir)
	// In order to actually support multi-threaded access to the git repo, we can't checkout any refs or assume that
	// the repo is checked out with any refspec. Instead, we use git cat-file to read the files that we need to the
	// memory and write them to a temp file. We need the temp file because the current version of Prow's config module
	// can't load the configs from the memory.

	// TODO must change git.Directory() to be the project-infra directory,
	// but maybe catFile cant work on remote ? and then we need some remote cat

	// need to http fetch
	// but what about unit tests ? they can use catFile but need to determine
	// maybe according git.Directory(), on unit tests its /tmp/gitrepo2772208634
	// strive to make it param, that either can be http or not, and check according that
	// if git.Directory() != "" {
	// 	panic(fmt.Sprintf("Panic: %s", git.Directory()))
	// }

	var prowConfigBytes, jobConfigBytes []byte
	prowLocation := h.prowLocation
	if prowLocation == "" {
		// TODO or unit tests, unless we create a dedicated unit tests folder(s)
		prowLocation = git.Directory()
		ret := 0
		prowConfigBytes, ret = catFile(log, prowLocation, h.prowConfigPath, "HEAD")
		if ret != 0 && ret != 128 {
			log.WithError(err).Errorf("Could not load Prow config %s", h.prowConfigPath)
			return nil, err
		}

		jobConfigBytes, ret = catFile(log, prowLocation, h.jobsConfigBase, "HEAD")
		if ret != 0 && ret != 128 {
			log.WithError(err).Errorf("Could not load Prow config %s", h.jobsConfigBase)
			return nil, err
		}
	} else {
		prowConfigUrl := prowLocation + "/" + h.prowConfigPath
		prowConfigBytes, err = fetchRemoteFile(prowConfigUrl)
		if err != nil {
			log.WithError(err).Errorf("Could not fetch prow config from %s", prowConfigUrl)
			return nil, err
		}

		org, repo, err := gitv2.OrgRepo(pr.Base.Repo.FullName)
		if err != nil {
			log.WithError(err).Errorf("Could not parse repo name: %s", pr.Base.Repo.FullName)
			return nil, err
		}

		branchAddon := ""
		if strings.HasPrefix(pr.Base.Ref, "release") {
			branchAddon = "-" + strings.TrimPrefix(pr.Base.Ref, "release")
		}

		// NOTE: only bracnhes main / master and release-<number> are supported, and must be yaml files (not yml)
		// for example kubevirt-presubmits-1.1.yaml will belong to release-1.1
		jobConfigUrl := prowLocation + "/" + h.jobsConfigBase + "/" + org + "/" +
			repo + "/" + repo + "-presubmits" + branchAddon + ".yaml"
		jobConfigBytes, err = fetchRemoteFile(jobConfigUrl)
		if err != nil {
			log.WithError(err).Errorf("Could not fetch prow config from %s", jobConfigUrl)
			return nil, err
		}
	}

	prowConfigTmp, err := writeTempFile(log, tmpdir, prowConfigBytes)
	if err != nil {
		log.WithError(err).Errorf("Could not write temporary Prow config.")
		return nil, err
	}

	// TODO read job config (build link of it), if it is not nil, allowed to be
	// load config, GetPresubmits / read static, filter them

	// jobsConfigBase
	// real - if prowLocation != "", then threat jobsConfigBase as folder
	// build the link, if file exists read it, else it is not error, just warn, and no presubmits added

	// unit tests - it used to filter what files changed to take from
	// now we need to just read it i think (and unit tests need to pass the file name itself that can be cat-file)

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

	var presumbits []config.Presubmit
	for _, jobs := range pc.PresubmitsStatic {
		for _, job := range jobs {
			presumbits = append(presumbits, job)
		}
	}

	err = listRequiredManual(h.ghClient, pr, presumbits)

	// TODO maybe we can use PresubmitsStatic once we read the files already ?
	// because it merged them
	// presubmits = cfg.GetPresubmitsStatic(orgRepo) or not because it need more annoying funcs

	// for _, changedJobConfig := range changedJobConfigs {
	// 	bytes, ret := catFile(log, git.Directory(), changedJobConfig, ref)
	// 	if ret == 128 {
	// 		// 128 means that the file was probably deleted in the PR or doesn't exists
	// 		// so to avoid an edge case where we need to take care of a null pointer, we
	// 		// generate an empty pc w/o jobs.
	// 		configs[changedJobConfig] = &config.Config{}
	// 		continue
	// 	} else if ret != 0 {
	// 		log.Errorf("Could not read job config from path %s at git ref: %s", changedJobConfig, ref)
	// 		return nil, fmt.Errorf("could not read job config from git ref")
	// 	}
	// 	jobConfigTmp, err := writeTempFile(log, tmpdir, bytes)
	// 	if err != nil {
	// 		log.WithError(err).Infoln("Could not write temp file")
	// 		return nil, err
	// 	}
	// 	pc, err := config.Load(prowConfigTmp, jobConfigTmp, nil, "")
	// 	if err != nil {
	// 		log.WithError(err).Errorf("Could not load job config from path %s at git ref %s", jobConfigTmp, ref)
	// 		return nil, err
	// 	}
	// 	// `config.Load` sets field `.JobBase.SourcePath` inside each job to the path from where the config was
	// 	// read, thus a deep equals can not succeed if two (otherwise identical) configs are read from different
	// 	// directories as we do here
	// 	// thus we need to reset the SourcePath to the original value for each job config
	// 	for _, presubmits := range pc.PresubmitsStatic {
	// 		for index, _ := range presubmits {
	// 			presubmits[index].JobBase.SourcePath = path.Join(git.Directory(), changedJobConfig)
	// 		}
	// 	}
	// 	configs[changedJobConfig] = pc
	// }

	return configs, nil
} */

// catFile executes a git cat-file command in the specified git dir and returns bytes representation of the file
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
	if pr.Draft {
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

func listRequested(ghClient githubClientInterface, pr github.PullRequest, requestedJobs []config.Presubmit) error {
	org, repo, err := gitv2.OrgRepo(pr.Base.Repo.FullName)
	if err != nil {
		log.WithError(err).Errorf("Could not parse repo name: %s", pr.Base.Repo.FullName)
		return err
	}

	if !(org == "kubevirt" && repo == "kubevirt") && !(org == "foo" && repo == "bar") {
		return nil
	}

	// If the PR is not mergeable (e.g. due to merge conflicts), we will not trigger any jobs,
	// to reduce the load on resources and reduce spam comments which will lead to a better review experience.
	if pr.Mergable != nil && !*pr.Mergable {
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

func manualRequiredFilter(p config.Presubmit) (bool, bool, bool) {
	return !p.Optional && !p.AlwaysRun && p.RegexpChangeMatcher.RunIfChanged == "" && p.RegexpChangeMatcher.SkipIfOnlyChanged == "",
		!p.Optional && !p.AlwaysRun && p.RegexpChangeMatcher.RunIfChanged == "" && p.RegexpChangeMatcher.SkipIfOnlyChanged == "", false
}
