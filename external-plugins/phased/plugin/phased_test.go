package main_test

import (
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/testing"
	"k8s.io/test-infra/prow/client/clientset/versioned/typed/prowjobs/v1/fake"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/git/localgit"
	git2 "k8s.io/test-infra/prow/git/v2"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/github/fakegithub"
	"k8s.io/test-infra/prow/labels"

	"kubevirt.io/project-infra/external-plugins/phased/plugin/handler"
)

const (
	org      = "foo"
	repo     = "bar"
	orgRepo  = org + "/" + repo
	prNumber = 17
)

var _ = Describe("Phased", func() {
	Context("A valid pull request event", func() {
		var gitrepo *localgit.LocalGit
		var gitClientFactory git2.ClientFactory

		BeforeEach(func() {
			var err error
			gitrepo, gitClientFactory, err = localgit.NewV2()
			Expect(err).ShouldNot(HaveOccurred())
		})

		AfterEach(func() {
			if gitClientFactory != nil {
				gitClientFactory.Clean()
			}
		})

		Context("lgtm label is added", func() {
			It("Should comment the manually required Prow jobs", func() {
				By("Creating a fake git repo", func() {
					makeRepoWithEmptyProwConfig(gitrepo, org, repo)
				})

				var baseref string
				By("Generating a base commit with jobs", func() {
					baseConfig, err := json.Marshal(&config.Config{
						JobConfig: config.JobConfig{
							PresubmitsStatic: map[string][]config.Presubmit{
								orgRepo: {
									{
										AlwaysRun: true,
										JobBase: config.JobBase{
											Name: "job_always_run",
											Spec: &v1.PodSpec{
												Containers: []v1.Container{
													{
														Image: "image1",
													},
												},
											},
										},
									},
									{
										JobBase: config.JobBase{
											Name: "job_always_run_false",
											Spec: &v1.PodSpec{
												Containers: []v1.Container{
													{
														Image: "image2",
													},
												},
											},
										},
									},
								},
							},
						},
					})
					Expect(err).ShouldNot(HaveOccurred())
					err = gitrepo.AddCommit(org, repo, map[string][]byte{
						"jobs-config.yaml": baseConfig,
					})
					Expect(err).ShouldNot(HaveOccurred())
					baseref, err = gitrepo.RevParse(org, repo, "HEAD")
					Expect(err).ShouldNot(HaveOccurred())
				})

				gh := fakegithub.NewFakeClient()
				gh.IssueLabelsExisting = append(gh.IssueLabelsExisting, issueLabels(labels.Approved)...)

				var event github.PullRequestEvent
				By("Generating a fake pull request event and registering it to the github client", func() {
					event = github.PullRequestEvent{
						Action: github.PullRequestActionLabeled,
						Label:  github.Label{Name: labels.LGTM},
						GUID:   "guid",
						Repo: github.Repo{
							FullName: orgRepo,
						},
						Sender: github.User{
							Login: "testuser",
						},
						PullRequest: github.PullRequest{
							Number: prNumber,
							State:  "open",
							Base: github.PullRequestBranch{
								Repo: github.Repo{
									Name:     repo,
									FullName: orgRepo,
								},
								Ref: baseref,
								SHA: baseref,
							},
							Head: github.PullRequestBranch{
								Repo: github.Repo{
									Name:     repo,
									FullName: orgRepo,
								},
								Ref: baseref,
								SHA: baseref,
							},
						},
					}

					gh.PullRequests = map[int]*github.PullRequest{
						prNumber: &event.PullRequest,
					}
				})

				By("Sending the event to the phased plugin server", func() {
					prowc := &fake.FakeProwV1{
						Fake: &testing.Fake{},
					}
					fakelog := logrus.New()
					eventsChan := make(chan *handler.GitHubEvent)
					eventsHandler := handler.NewGitHubEventsHandler(
						eventsChan,
						fakelog,
						prowc.ProwJobs("test-ns"),
						gh,
						"prowconfig.yaml",
						"jobs-config.yaml",
						"",
						gitClientFactory)

					handlerEvent, err := makeHandlerPullRequestEvent(&event)
					Expect(err).ShouldNot(HaveOccurred())

					eventsHandler.SetLocalConfLoad()
					eventsHandler.Handle(handlerEvent)

					Expect(len(gh.IssueCommentsAdded)).To(Equal(1), "Expected github comment to be added")
					Expect(gh.IssueCommentsAdded[0]).To(Equal(fmt.Sprintf("%s#%d:/test job_always_run_false\n", orgRepo, prNumber)))
				})

			})

		})

	})

})

func makeRepoWithEmptyProwConfig(lg *localgit.LocalGit, org, repo string) error {
	err := lg.MakeFakeRepo(org, repo)
	if err != nil {
		return err
	}
	prowConfig, err := json.Marshal(&config.ProwConfig{})
	if err != nil {
		return err
	}
	return lg.AddCommit(org, repo, map[string][]byte{
		"prowconfig.yaml": prowConfig,
	})
}

func makeHandlerPullRequestEvent(event *github.PullRequestEvent) (*handler.GitHubEvent, error) {
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	handlerEvent := &handler.GitHubEvent{
		Type:    "pull_request",
		GUID:    event.GUID,
		Payload: eventBytes,
	}
	return handlerEvent, nil
}

func issueLabels(labels ...string) []string {
	var ls []string
	for _, label := range labels {
		ls = append(ls, fmt.Sprintf("%s#%d:%s", orgRepo, prNumber, label))
	}
	return ls
}
