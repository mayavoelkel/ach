// Licensed to The Moov Authors under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. The Moov Authors licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package ach

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var errDestinationFull = errors.New("destination refused more data")

// limitedWriter accepts the first limit bytes and then fails, like a full disk or dropped connection.
type limitedWriter struct {
	buf   bytes.Buffer
	limit int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	room := lw.limit - lw.buf.Len()
	if room <= 0 {
		return 0, errDestinationFull
	}
	if len(p) > room {
		lw.buf.Write(p[:room])
		return room, errDestinationFull
	}
	return lw.buf.Write(p)
}

// writerFailureFixtures returns files that together contain every record type Writer can emit.
func writerFailureFixtures(t *testing.T) map[string]*File {
	t.Helper()

	ppd := mockFilePPD(t)

	everyAddenda := NewFile().SetHeader(mockFileHeader())
	entry := mockEntryDetail()
	entry.AddendaRecordIndicator = 1
	entry.Addenda02 = mockAddenda02()
	entry.AddAddenda05(mockAddenda05())
	entry.AddAddenda05(mockAddenda05())
	entry.Addenda98 = mockAddenda98()
	entry.Addenda98Refused = mockAddenda98Refused()
	entry.Addenda99 = mockAddenda99()
	entry.Addenda99Dishonored = mockAddenda99Dishonored()
	entry.Addenda99Contested = mockAddenda99Contested()
	batch := NewBatchPPD(mockBatchPPDHeader())
	batch.AddEntry(entry)
	everyAddenda.AddBatch(batch)
	everyAddenda.Control = mockFileControl()

	adv := mockFileADV(t)
	adv.Batches[0].GetADVEntries()[0].Addenda99 = mockAddenda99()

	iat := NewFile().SetHeader(mockFileHeader())
	iatBatch := NewIATBatch(mockIATBatchHeaderFF())
	iatEntry := mockIATEntryDetail()
	iatEntry.Addenda10 = mockAddenda10()
	iatEntry.Addenda11 = mockAddenda11()
	iatEntry.Addenda12 = mockAddenda12()
	iatEntry.Addenda13 = mockAddenda13()
	iatEntry.Addenda14 = mockAddenda14()
	iatEntry.Addenda15 = mockAddenda15()
	iatEntry.Addenda16 = mockAddenda16()
	iatEntry.AddAddenda17(mockAddenda17())
	iatEntry.AddAddenda17(mockAddenda17B())
	iatEntry.AddAddenda18(mockAddenda18())
	iatEntry.AddAddenda18(mockAddenda18B())
	iatEntry.Addenda98 = mockAddenda98()
	iatEntry.Addenda99 = mockAddenda99()
	iatBatch.AddEntry(iatEntry)
	iat.AddIATBatch(iatBatch)
	iat.Control = mockFileControl()

	return map[string]*File{
		"PPD":                      ppd,
		"entry with every addenda": everyAddenda,
		"ADV with return":          adv,
		"IAT with every addenda":   iat,
	}
}

// writerFixtureRecords lists, in file order, every record the writer is expected to output for file.
func writerFixtureRecords(file *File) []writeEntry {
	records := []writeEntry{&file.Header}
	for _, batch := range file.Batches {
		records = append(records, batch.GetHeader())
		for _, e := range batch.GetEntries() {
			records = append(records, e)
			for _, a := range []writeEntry{e.Addenda02, e.Addenda98, e.Addenda98Refused, e.Addenda99, e.Addenda99Dishonored, e.Addenda99Contested} {
				if !isNilRecord(a) {
					records = append(records, a)
				}
			}
			for _, a := range e.Addenda05 {
				records = append(records, a)
			}
		}
		for _, e := range batch.GetADVEntries() {
			records = append(records, e)
			if e.Addenda99 != nil {
				records = append(records, e.Addenda99)
			}
		}
	}
	for _, batch := range file.IATBatches {
		records = append(records, batch.GetHeader())
		for _, e := range batch.GetEntries() {
			records = append(records, e)
			for _, a := range []writeEntry{e.Addenda10, e.Addenda11, e.Addenda12, e.Addenda13, e.Addenda14, e.Addenda15, e.Addenda16, e.Addenda98, e.Addenda99} {
				if !isNilRecord(a) {
					records = append(records, a)
				}
			}
			for _, a := range e.Addenda17 {
				records = append(records, a)
			}
			for _, a := range e.Addenda18 {
				records = append(records, a)
			}
		}
	}
	if file.IsADV() {
		records = append(records, &file.ADVControl)
	} else {
		records = append(records, &file.Control)
	}
	return records
}

func isNilRecord(r writeEntry) bool {
	if r == nil {
		return true
	}
	v := reflect.ValueOf(r)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

func writeFileForTest(t *testing.T, file *File, lineEnding string) string {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriterWithOpts(&buf, &WriteOpts{LineEnding: lineEnding})
	w.BypassValidation = true
	require.NoError(t, w.Write(file))
	return buf.String()
}

func TestWriter_FixturesProduceEveryRecordType(t *testing.T) {
	fixtures := writerFailureFixtures(t)
	var all strings.Builder
	for name, file := range fixtures {
		out := writeFileForTest(t, file, "\n")
		for i, record := range writerFixtureRecords(file) {
			require.Contains(t, out, record.String()+"\n", "%s: record %d (%T) missing from output", name, i, record)
		}
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		require.Zero(t, len(lines)%10, "%s: output must be padded to a block of 10 lines", name)
		for i, line := range lines {
			require.Len(t, line, RecordLength, "%s: line %d", name, i+1)
		}
		all.WriteString(out)
	}

	// Every record type and addenda type that Writer has a branch for must appear at least once.
	for _, prefix := range []string{
		"1", "5", "6", "8", "9",
		"702", "705", "798", "799",
		"710", "711", "712", "713", "714", "715", "716", "717", "718",
	} {
		require.Contains(t, "\n"+all.String(), "\n"+prefix, "missing record starting with %q", prefix)
	}
}

// TestWriter_FailsAtEveryByte stops the destination at every possible byte offset and checks
// that Write reports the destination's error and never writes anything other than a prefix
// of the correct file.
func TestWriter_FailsAtEveryByte(t *testing.T) {
	for name, file := range writerFailureFixtures(t) {
		for _, lineEnding := range []string{"\n", "\r\n"} {
			t.Run(fmt.Sprintf("%s/%q", name, lineEnding), func(t *testing.T) {
				expected := writeFileForTest(t, file, lineEnding)

				for limit := 0; limit < len(expected); limit++ {
					dst := &limitedWriter{limit: limit}
					w := NewWriterWithOpts(dst, &WriteOpts{LineEnding: lineEnding})
					w.BypassValidation = true
					// A 1-byte buffer makes each failure surface on the record being written.
					w.w = bufio.NewWriterSize(dst, 1)

					err := w.Write(file)
					require.ErrorIs(t, err, errDestinationFull, "limit=%d", limit)
					require.Equal(t, expected[:limit], dst.buf.String(), "limit=%d", limit)
				}

				dst := &limitedWriter{limit: len(expected)}
				w := NewWriterWithOpts(dst, &WriteOpts{LineEnding: lineEnding})
				w.BypassValidation = true
				w.w = bufio.NewWriterSize(dst, 1)
				require.NoError(t, w.Write(file))
				require.Equal(t, expected, dst.buf.String())
			})
		}
	}
}

// TestWriter_FailsWithDefaultBuffer checks failures are reported with the normal NewWriter setup,
// including a file large enough that the writer flushes in the middle of the file.
func TestWriter_FailsWithDefaultBuffer(t *testing.T) {
	file := NewFile().SetHeader(mockFileHeader())
	batch := NewBatchPPD(mockBatchPPDHeader())
	for i := 0; i < 60; i++ {
		entry := mockEntryDetail()
		entry.SetTraceNumber(mockBatchPPDHeader().ODFIIdentification, i+1)
		batch.AddEntry(entry)
	}
	require.NoError(t, batch.Create())
	file.AddBatch(batch)
	require.NoError(t, file.Create())

	var full bytes.Buffer
	require.NoError(t, NewWriter(&full).Write(file))
	expected := full.String()
	require.Greater(t, len(expected), 4096, "file must be larger than the default buffer")

	for _, limit := range []int{0, 1, 94, 4095, 4096, 4097, len(expected) - 1} {
		dst := &limitedWriter{limit: limit}
		err := NewWriter(dst).Write(file)
		require.ErrorIs(t, err, errDestinationFull, "limit=%d", limit)
		require.Equal(t, expected[:limit], dst.buf.String(), "limit=%d", limit)
	}
}

func TestWriter_InvalidFileWritesNothing(t *testing.T) {
	file := mockFilePPD(t)
	file.Header.FileCreationDate = "abc"

	var buf bytes.Buffer
	err := NewWriter(&buf).Write(file)
	require.Error(t, err)
	require.NotErrorIs(t, err, errDestinationFull)
	require.Zero(t, buf.Len())
}

func TestWriter_FlushWithoutDestination(t *testing.T) {
	var nilWriter *Writer
	require.EqualError(t, nilWriter.Flush(), "nil writer")
	require.EqualError(t, (&Writer{}).Flush(), "nil writer")
}
