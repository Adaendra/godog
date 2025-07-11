package parser

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"

	gherkin "github.com/cucumber/gherkin/go/v32"
	messages "github.com/cucumber/messages/go/v24"

	"github.com/cucumber/godog/internal/flags"
	"github.com/cucumber/godog/internal/models"
	"github.com/cucumber/godog/internal/tags"
)

var pathLineRe = regexp.MustCompile(`:([\d]+)$`)

// ExtractFeaturePathLine ...
func ExtractFeaturePathLine(p string) (string, int) {
	line := -1
	retPath := p
	if m := pathLineRe.FindStringSubmatch(p); len(m) > 0 {
		if i, err := strconv.Atoi(m[1]); err == nil {
			line = i
			retPath = p[:strings.LastIndexByte(p, ':')]
		}
	}
	return retPath, line
}

func parseFeatureFile(fsys fs.FS, path, dialect string, newIDFunc func() string) (*models.Feature, error) {
	reader, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}

	defer reader.Close()

	var buf bytes.Buffer
	gherkinDocument, err := gherkin.ParseGherkinDocumentForLanguage(io.TeeReader(reader, &buf), dialect, newIDFunc)
	if err != nil {
		return nil, fmt.Errorf("%s - %v", path, err)
	}

	gherkinDocument.Uri = path
	pickles := gherkin.Pickles(*gherkinDocument, path, newIDFunc)

	f := models.Feature{GherkinDocument: gherkinDocument, Pickles: pickles, Content: buf.Bytes()}
	return &f, nil
}

func parseBytes(path string, feature []byte, dialect string, newIDFunc func() string) (*models.Feature, error) {
	reader := bytes.NewReader(feature)

	var buf bytes.Buffer
	gherkinDocument, err := gherkin.ParseGherkinDocumentForLanguage(io.TeeReader(reader, &buf), dialect, newIDFunc)
	if err != nil {
		return nil, fmt.Errorf("%s - %v", path, err)
	}

	gherkinDocument.Uri = path
	pickles := gherkin.Pickles(*gherkinDocument, path, newIDFunc)

	f := models.Feature{GherkinDocument: gherkinDocument, Pickles: pickles, Content: buf.Bytes()}
	return &f, nil
}

func parseFeatureDir(fsys fs.FS, dir, dialect string, newIDFunc func() string) ([]*models.Feature, error) {
	var features []*models.Feature
	return features, fs.WalkDir(fsys, dir, func(p string, f fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if f.IsDir() {
			return nil
		}

		if !strings.HasSuffix(p, ".feature") {
			return nil
		}

		feat, err := parseFeatureFile(fsys, p, dialect, newIDFunc)
		if err != nil {
			return err
		}

		features = append(features, feat)
		return nil
	})
}

func parsePath(fsys fs.FS, path, dialect string, newIDFunc func() string) ([]*models.Feature, error) {
	var features []*models.Feature

	path, line := ExtractFeaturePathLine(path)

	fi, err := func() (fs.FileInfo, error) {
		file, err := fsys.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		return file.Stat()
	}()
	if err != nil {
		return features, err
	}

	if fi.IsDir() {
		return parseFeatureDir(fsys, path, dialect, newIDFunc)
	}

	ft, err := parseFeatureFile(fsys, path, dialect, newIDFunc)
	if err != nil {
		return features, err
	}

	// filter scenario by line number
	var pickles []*messages.Pickle

	if line != -1 {
		ft.Uri += ":" + strconv.Itoa(line)
	}

	for _, pickle := range ft.Pickles {
		sc := ft.FindScenario(pickle.AstNodeIds[0])

		if line == -1 || int64(line) == sc.Location.Line {
			if line != -1 {
				pickle.Uri += ":" + strconv.Itoa(line)
			}

			pickles = append(pickles, pickle)
		}
	}
	ft.Pickles = pickles

	return append(features, ft), nil
}

// ParseFeatures ...
func ParseFeatures(fsys fs.FS, filter, dialect string, paths []string) ([]*models.Feature, error) {
	var order int

	if dialect == "" {
		dialect = gherkin.DefaultDialect
	}

	featureIdxs := make(map[string]int)
	uniqueFeatureURI := make(map[string]*models.Feature)
	newIDFunc := (&messages.Incrementing{}).NewId
	for _, path := range paths {
		feats, err := parsePath(fsys, path, dialect, newIDFunc)

		switch {
		case os.IsNotExist(err):
			return nil, fmt.Errorf(`feature path "%s" is not available`, path)
		case os.IsPermission(err):
			return nil, fmt.Errorf(`feature path "%s" is not accessible`, path)
		case err != nil:
			return nil, err
		}

		for _, ft := range feats {
			if _, duplicate := uniqueFeatureURI[ft.Uri]; duplicate {
				continue
			}

			uniqueFeatureURI[ft.Uri] = ft
			featureIdxs[ft.Uri] = order

			order++
		}
	}

	var features = make([]*models.Feature, len(uniqueFeatureURI))
	for uri, feature := range uniqueFeatureURI {
		idx := featureIdxs[uri]
		features[idx] = feature
	}

	features = filterFeatures(filter, features)

	return features, nil
}

type FeatureContent = flags.Feature

func ParseFromBytes(filter, dialect string, featuresInputs []FeatureContent) ([]*models.Feature, error) {
	var order int

	if dialect == "" {
		dialect = gherkin.DefaultDialect
	}

	featureIdxs := make(map[string]int)
	uniqueFeatureURI := make(map[string]*models.Feature)
	newIDFunc := (&messages.Incrementing{}).NewId
	for _, f := range featuresInputs {
		ft, err := parseBytes(f.Name, f.Contents, dialect, newIDFunc)
		if err != nil {
			return nil, err
		}

		if _, duplicate := uniqueFeatureURI[ft.Uri]; duplicate {
			continue
		}

		uniqueFeatureURI[ft.Uri] = ft
		featureIdxs[ft.Uri] = order

		order++
	}

	var features = make([]*models.Feature, len(uniqueFeatureURI))
	for uri, feature := range uniqueFeatureURI {
		idx := featureIdxs[uri]
		features[idx] = feature
	}

	features = filterFeatures(filter, features)

	return features, nil
}

func filterFeatures(filter string, features []*models.Feature) (result []*models.Feature) {
	for _, ft := range features {
		ft.Pickles = tags.ApplyTagFilter(filter, ft.Pickles)

		if ft.Feature != nil && len(ft.Pickles) > 0 {
			result = append(result, ft)
		}
	}

	return
}
