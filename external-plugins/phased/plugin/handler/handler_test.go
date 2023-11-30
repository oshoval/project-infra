package handler

import (
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/git/localgit"
	gitv2 "k8s.io/test-infra/prow/git/v2"
)

var _ = Describe("Events", func() {

	Context("With a git repo", func() {
		var gitrepo *localgit.LocalGit
		var gitClientFactory gitv2.ClientFactory
		//var eventsServer *GitHubEventsHandler
		//var dummyLog *logrus.Logger

		BeforeEach(func() {

			var err error
			gitrepo, gitClientFactory, err = localgit.NewV2()
			Expect(err).ShouldNot(HaveOccurred(), "Could not create local git repo and client factory")
			//dummyLog = logrus.New()
			// eventsServer = NewGitHubEventsHandler(
			// 	nil,
			// 	dummyLog,
			// 	nil,
			// 	nil,
			// 	"prow-config.yaml",
			// 	"",
			// 	"",
			// 	gitClientFactory)
		})

		AfterEach(func() {
			if gitClientFactory != nil {
				gitClientFactory.Clean()
			}
		})

		AfterEach(func() {
			if gitrepo != nil {
				os.RemoveAll(gitrepo.Dir)
			}
		})

		It("Should load jobs from git refspec", func() {
			prowConfig := config.ProwConfig{}
			jobsConfig := config.JobConfig{
				PresubmitsStatic: map[string][]config.Presubmit{
					"foo/bar": {
						{
							JobBase: config.JobBase{
								Name: "a-presubmit",
								Spec: &v1.PodSpec{
									Containers: []v1.Container{
										{
											Image:   "foo/var",
											Command: []string{"/bin/foo"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(gitrepo.MakeFakeRepo("foo", "bar")).Should(Succeed())
			prowConfigBytes, err := json.Marshal(prowConfig)
			Expect(err).ShouldNot(HaveOccurred())
			jobsConfigBytes, err := json.Marshal(jobsConfig)
			Expect(err).ShouldNot(HaveOccurred())
			files := map[string][]byte{
				"prow-config.yaml": prowConfigBytes,
				"jobs-config.yaml": jobsConfigBytes,
			}
			Expect(gitrepo.AddCommit("foo", "bar", files)).Should(Succeed())
			// TODO maybe use this test

			// headref, err := gitrepo.RevParse("foo", "bar", "HEAD")
			// Expect(err).ShouldNot(HaveOccurred())
			// gitClient, err := gitClientFactory.ClientFor("foo", "bar")
			// Expect(err).ShouldNot(HaveOccurred())
			// out, err := eventsServer.loadConfigsAtRef([]string{"jobs-config.yaml"}, gitClient, headref)
			// Expect(err).ShouldNot(HaveOccurred())
			// outConfig, exists := out["jobs-config.yaml"]
			// Expect(exists).To(BeTrue())
			// outJobs, exists := outConfig.PresubmitsStatic["foo/bar"]
			// Expect(exists).To(BeTrue())
			// Expect(outJobs[0].Name).To(Equal(jobsConfig.PresubmitsStatic["foo/bar"][0].Name))
		})

	})

	Context("Utility functions", func() {

		// It("Should return correct repo from job key", func() {
		// 	ret := repoFromJobKey("foo/bar#baz-something/something-else")
		// 	Expect(ret).To(Equal("foo/bar"))
		// })

		/*
			DescribeTable(
				"Should calculate extra refs",
				func(refs []prowapi.Refs, expected prowapi.Refs) {
					ret := makeTargetRepoRefs(refs, "foo", "bar", "baz")
					Expect(ret).To(Equal(expected))
					Expect(refs).ToNot(Equal(expected), "Input refs should not be modified")
				},
				Entry(
					"Refs exists and there is no workdir defined",
					[]prowapi.Refs{
						{
							WorkDir: false,
						},
					},
					prowapi.Refs{
						Org:     "foo",
						Repo:    "bar",
						WorkDir: true,
						BaseRef: "baz",
					},
				),
				Entry(
					"Refs is nil",
					nil,
					prowapi.Refs{
						Org:     "foo",
						Repo:    "bar",
						WorkDir: true,
						BaseRef: "baz",
					},
				),
			)*/

		/*
			DescribeTable(
				"Should calculate if a workdir is already defined",
				func(refs []prowapi.Refs, expected bool) {
					Expect(workdirAlreadyDefined(refs)).To(Equal(expected))
				},
				Entry(
					"When workdir is already defined",
					[]prowapi.Refs{
						{
							WorkDir: false,
						},
						{
							WorkDir: true,
						},
					},
					true),
				Entry(
					"When workdir is not defined",
					[]prowapi.Refs{
						{
							WorkDir: false,
						},
						{
							WorkDir: false,
						},
					},
					false),
			) */

	})

})
