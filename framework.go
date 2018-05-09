package aloe

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/caicloud/aloe/cleaner"
	"github.com/caicloud/aloe/data"
	"github.com/caicloud/aloe/preset"
	"github.com/caicloud/aloe/roundtrip"
	"github.com/caicloud/aloe/types"
	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
)

// Framework defines an API test framework
type Framework interface {
	// RegisterCleaner registers cleaner of framework
	RegisterCleaner(cs ...cleaner.Cleaner) error
	// RegisterPresetter registers presetter of framework
	RegisterPresetter(ps ...preset.Presetter) error
	// Run will run the framework
	Run(t *testing.T)
}

// NewFramework returns an API test framework
func NewFramework(host string, dataDirs ...string) Framework {
	reqHeader := preset.NewHeaderPresetter(preset.RequestType)
	respHeader := preset.NewHeaderPresetter(preset.ResponseType)
	return &genericFramework{
		dataDirs: dataDirs,
		client:   roundtrip.NewClient(host),
		cleaners: map[string]cleaner.Cleaner{},
		presetters: map[string]preset.Presetter{
			reqHeader.Name():  reqHeader,
			respHeader.Name(): respHeader,
		},
	}
}

type genericFramework struct {
	dataDirs []string

	client *roundtrip.Client

	cleaners map[string]cleaner.Cleaner

	presetters map[string]preset.Presetter
}

// RegisterCleaner implements Framework interface
func (gf *genericFramework) RegisterCleaner(cs ...cleaner.Cleaner) error {
	for _, c := range cs {
		if _, ok := gf.cleaners[c.Name()]; ok {
			return fmt.Errorf("can't register cleaner %v: already exists", c.Name())
		}
		gf.cleaners[c.Name()] = c
	}
	return nil
}

// RegisterPresetter implements Framework interface
func (gf *genericFramework) RegisterPresetter(ps ...preset.Presetter) error {
	for _, p := range ps {
		if _, ok := gf.presetters[p.Name()]; ok {
			return fmt.Errorf("can't register presetter %v: already exists", p.Name())
		}
		gf.presetters[p.Name()] = p
	}
	return nil
}

func (gf *genericFramework) Run(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	for _, r := range gf.dataDirs {
		dir, err := data.Walk(r)
		if err != nil {
			t.Fatalf(err.Error())
			return
		}
		ctx := &types.Context{}
		f := gf.walk(ctx, dir)
		ginkgo.Describe(dir.Context.Summary, f)
	}
	ginkgo.RunSpecs(t, "Test Suit")
}

func (gf *genericFramework) walk(ctx *types.Context, dir *data.Dir) func() {
	dirs, files := dir.Dirs, dir.Files
	ctxConfig := dir.Context
	total := dir.CaseNum

	return func() {
		var curContext *types.Context
		count := 0
		lock := sync.Mutex{}

		for name, d := range dirs {
			f := gf.walk(ctx, &d)
			summary := genSummary(name, d.Context.Summary)
			ginkgo.Context(summary, f)
		}
		for name, c := range files {
			summary := genSummary(name, c.Case.Description)
			f := gf.itFunc(ctx, &c)
			ginkgo.It(summary, f)
		}

		ginkgo.BeforeEach(func() {
			lock.Lock()
			defer lock.Unlock()
			if count == 0 {
				// construct context from context config file
				gomega.Expect(gf.constructContext(ctx, &ctxConfig, false)).
					NotTo(gomega.HaveOccurred())
			} else {
				gomega.Expect(gf.constructContext(ctx, &ctxConfig, true)).
					NotTo(gomega.HaveOccurred())
			}
			curContext = saveContext(ctx)
		})

		ginkgo.AfterEach(func() {
			// restore context
			restoreContext(ctx, curContext)

			lock.Lock()
			defer lock.Unlock()
			count++
			if count == total {
				cleaner, ok := gf.cleaners[ctx.CleanerName]
				if ok {
					gomega.Expect(cleaner.Clean(ctx.Variables)).NotTo(gomega.HaveOccurred())
				}
			}
		})
	}
}

func genSummary(name, summary string) string {
	return name + ": " + summary
}

var (
	defaultTimeout  = 1 * time.Second
	defaultInterval = 100 * time.Millisecond
)

func (gf *genericFramework) itFunc(ctx *types.Context, file *data.File) func() {
	c := file.Case
	return func() {
		for _, rt := range c.Flow {
			nrt := roundtrip.MergeRoundTrip(ctx.RoundTripTemplate, &rt)
			ginkgo.By(nrt.Description)
			respMatcher, err := roundtrip.MatchResponse(ctx, nrt)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			if ev := nrt.Response.Eventually; ev != nil {
				timeout := ev.Timeout
				if timeout == nil {
					timeout = &types.Duration{
						Duration: defaultTimeout,
					}
				}
				interval := ev.Interval
				if interval == nil {
					interval = &types.Duration{
						Duration: defaultInterval,
					}
				}
				gomega.Eventually(func() *http.Response {
					resp, err := gf.client.DoRequest(ctx, nrt)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return resp
				}, timeout.Duration, interval.Duration).Should(respMatcher)

			} else {
				resp, err := gf.client.DoRequest(ctx, nrt)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(resp).To(respMatcher)
			}
			vs, err := respMatcher.Variables()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			for k, v := range vs {
				ctx.Variables[k] = v
			}
		}
	}
}
