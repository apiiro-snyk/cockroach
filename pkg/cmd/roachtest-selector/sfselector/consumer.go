// Copyright 2024 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// consumer is responsible for reading the csv files containing the test details generated by testselector

package sfselector

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"

	"cloud.google.com/go/storage"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/registry"
	"github.com/cockroachdb/errors"
	"google.golang.org/api/option"
)

// testInfo captures the information available from the csv
type testInfo struct {
	selected            bool  // whether a test is selected or not
	avgDurationInMillis int64 // average duration of the test
	totalRuns           int   // total number of times the test has run
}

// ReadTestsToRun reads the tests to run based on certain criteria:
// 1. the number of time a test has been successfully running
// 2. the test is new
// 3. the test has not been run for a while
// 4. a subset of the successful tests
// The individual tests in the input are modified with the skip reason.
// It returns the number of tests that satisfied the selection criteria and have been modified.
func ReadTestsToRun(
	ctx context.Context, tests []registry.TestSpec, cloud, suite string,
) (int, error) {
	options := []option.ClientOption{option.WithScopes(storage.ScopeReadOnly), option.WithQuotaProject(project)}
	cj := os.Getenv("GOOGLE_EPHEMERAL_CREDENTIALS")
	if len(cj) != 0 {
		options = append(options, option.WithCredentialsJSON([]byte(cj)))
	} else {
		fmt.Printf("GOOGLE_EPHEMERAL_CREDENTIALS env is not set.\n")
	}
	client, err := storage.NewClient(ctx, options...)
	if err != nil {
		return len(tests), errors.NewAssertionErrorWithWrappedErrf(err, "connection to GCS failed")
	}
	defer func() { _ = client.Close() }()

	object := fmt.Sprintf("%s-%s-%s.%s", testsFileLocation, suite, cloud, testsCsvExtension)
	r, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return len(tests), errors.NewAssertionErrorWithWrappedErrf(err,
			"failed to get the object %s in bucket %s", object, bucket)
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(r)
	if err != nil {
		return len(tests), errors.NewAssertionErrorWithWrappedErrf(err, "failed to read CSV from GCS")
	}
	cr := csv.NewReader(bytes.NewReader(body))
	data, err := cr.ReadAll()
	if err != nil {
		return len(tests), errors.NewAssertionErrorWithWrappedErrf(err, "failed to read CSV data")
	}
	testNamesToRun := make(map[string]*testInfo)
	if len(data) <= 1 {
		// the file has just the header
		return len(tests), nil
	}
	// csv columns:
	// 0. TEST_NAME
	// 1. SELECTED (yes/no)
	// 2. AVG_DURATION
	// 3. TOTAL_RUNS
	for _, d := range data[1:] {
		testNamesToRun[d[0]] = &testInfo{
			selected:            d[1] != "no",
			avgDurationInMillis: getDuration(d[2]),
			totalRuns:           getTotalRuns(d[3]),
		}
	}
	selectedTestsCount := 0
	for i := range tests {
		if testShouldBeSkipped(testNamesToRun, tests[i], suite) {
			tests[i].Skip = "test selector"
			tests[i].SkipDetails = "test skipped because it is stable and selective-tests is set."
		} else {
			selectedTestsCount++
		}
	}
	return selectedTestsCount, nil
}

// getDuration extracts the duration from the csv data
func getDuration(durationStr string) int64 {
	duration, _ := strconv.ParseInt(durationStr, 10, 64)
	return duration
}

// getTotalRuns extracts the total runs from the csv data
func getTotalRuns(totalRunsStr string) int {
	totalRuns, _ := strconv.ParseInt(totalRunsStr, 10, 64)
	return int(totalRuns)
}

// testShouldBeSkipped decides whether a test should be skipped based on testNamesToRun and suite
func testShouldBeSkipped(
	testNamesToRun map[string]*testInfo, test registry.TestSpec, suite string,
) bool {
	for test.TestSelectionOptOutSuites.IsInitialized() && test.TestSelectionOptOutSuites.Contains(suite) {
		// test should not be skipped for this suite
		return false
	}
	toRun, ok := testNamesToRun[test.Name]
	return ok && test.Skip == "" && !toRun.selected
}
