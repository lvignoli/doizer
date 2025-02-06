package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/carlmjohnson/requests"
	"github.com/charmbracelet/log"
	"github.com/nickng/bibtex"
	"github.com/sourcegraph/conc/stream"
	"github.com/tidwall/gjson"
	"github.com/urfave/cli/v3"
	"golang.org/x/time/rate"
)

var ErrDOIExists = errors.New("DOI exists")

type WrongTitleError struct {
	expected string
	actual   string
}

func (e WrongTitleError) Error() string {
	return fmt.Sprintf("title don't match: expected %q, actual %q", e.expected, e.actual)
}

func main() {
	cmd := cli.Command{
		Name:      "doizer",
		Usage:     "Add missing DOIs to your bibtex files.",
		UsageText: "doizer -i <INPUT.bib> -o <OUTPUT.bib>",
		Description: `For all entries of the input bibtex file with a missing DOI, queries crossref
with the title, authors and date for highest scoring reference and picks its
DOI. Flags any case where title differs.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:      "input",
				Aliases:   []string{"i"},
				Usage:     "input file",
				OnlyOnce:  true,
				Required:  true,
				TakesFile: true,
			},
			&cli.StringFlag{
				Name:      "output",
				Aliases:   []string{"o"},
				Usage:     "output file",
				OnlyOnce:  true,
				Required:  true,
				TakesFile: true,
			},
		},
		Action: action,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}

func action(ctx context.Context, c *cli.Command) error {
	inputFile := c.String("input")
	f, err := os.Open(inputFile)
	if err != nil {
		return fmt.Errorf("%s: %w", inputFile, err)
	}

	refs, err := bibtex.Parse(f)
	if err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	s := stream.New()
	for i, e := range refs.Entries {
		e := e
		s.Go(func() stream.Callback {
			return func() {
				err := process(e)
				prefix := fmt.Sprintf("%03d: %s", i, e.CiteName)
				log := log.WithPrefix(prefix)

				if err == nil {
					log.Infof("Got DOI %s", e.Fields["doi"])
				} else if errors.Is(err, ErrDOIExists) {
					log.Infof("skipped: existing DOI: %s", e.Fields["doi"])
				} else if errors.As(err, &WrongTitleError{}) {
					log.Infof("Got DOI %s", e.Fields["doi"])
					log.Warnf("%v", err.(WrongTitleError))
				} else {
					log.Errorf("%v", err)
				}
			}
		})
	}
	s.Wait()

	out := refs.PrettyString()
	if err := os.WriteFile(c.String("output"), []byte(out), 0755); err != nil {
		return err
	}

	return nil
}

func process(e *bibtex.BibEntry) error {
	if _, ok := e.Fields["doi"]; ok {
		return ErrDOIExists
	}
	DOI, err := getDOI(context.Background(), e)
	e.Fields["doi"] = bibtex.NewBibConst(DOI)
	return err
}

var l = rate.NewLimiter(10, 10)

func RateLimitTransport(ctx context.Context, rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return requests.RoundTripFunc(func(req *http.Request) (res *http.Response, err error) {
		if err := l.Wait(ctx); err != nil {
			return nil, errors.New("context was cancelled")
		}

		return rt.RoundTrip(req)
	})
}

func bibliographyLikeString(e *bibtex.BibEntry) string {
	var fields []string
	if t, ok := e.Fields["title"]; ok {
		fields = append(fields, t.String())
	}
	if a, ok := e.Fields["author"]; ok {
		fields = append(fields, a.String())
	}
	if y, ok := e.Fields["year"]; ok {
		fields = append(fields, y.String())
	}

	fields = append([]string{"\""}, fields...)
	fields = append(fields, "\"")

	return strings.Join(fields, ", ")
}

func getDOI(ctx context.Context, e *bibtex.BibEntry) (string, error) {
	title := e.Fields["title"].String()
	q := bibliographyLikeString(e)

	var s string

	r := requests.URL("api.crossref.org/works").
		Transport(RateLimitTransport(ctx, nil)).
		ParamInt("rows", 5).
		Param("query.bibliographic", q).
		Param("sort", "score").
		Param("order", "desc").
		Accept("application/json").
		ToString(&s)

	if err := r.Fetch(ctx); err != nil {
		return "", err
	}

	bestScore := 0
	DOI := ""
	titleFromCrossref := ""

	gjson.Get(s, "message.items").ForEach(func(key, value gjson.Result) bool {
		d := value.Get("DOI").Str
		t := value.Get("title.0").Str
		score := int(value.Get("score").Int())

		if score > bestScore {
			bestScore = score
			DOI = d
			titleFromCrossref = t
		}

		return true
	})

	if DOI == "" {
		return "", errors.New("no DOI found")
	}

	if strings.ToLower(titleFromCrossref) != strings.ToLower(title) {
		return DOI, WrongTitleError{expected: title, actual: titleFromCrossref}
	}

	return DOI, nil
}
