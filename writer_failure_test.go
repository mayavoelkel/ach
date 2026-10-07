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
	"testing"

	"github.com/stretchr/testify/require"
)

var errDestinationFull = errors.New("destination full")

// limitedWriter accepts limit bytes and then fails, like a disk filling up or a dropped connection.
type limitedWriter struct {
	limit           int
	buf             bytes.Buffer
	failed          bool
	writesAfterFail int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.failed {
		lw.writesAfterFail++
		return 0, errDestinationFull
	}
	room := lw.limit - lw.buf.Len()
	if len(p) > room {
		lw.buf.Write(p[:room])
		lw.failed = true
		return room, errDestinationFull
	}
	return lw.buf.Write(p)
}

// failureTestFileWithEveryRecord holds a domestic batch whose entry carries every
// kind of addenda record, plus an IAT batch whose entry carries every IAT addenda.
func failureTestFileWithEveryRecord() *File {
	file := NewFile().SetHeader(mockFileHeader())

	entry := mockEntryDetail()
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
	file.AddBatch(batch)

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
	iatBatch := NewIATBatch(mockIATBatchHeaderFF())
	iatBatch.AddEntry(iatEntry)
	file.AddIATBatch(iatBatch)

	return file
}

func failureTestADVFile() *File {
	file := NewFile().SetHeader(mockFileHeader())
	entry := mockADVEntryDetail()
	entry.Addenda99 = mockAddenda99()
	batch := NewBatchADV(mockBatchADVHeader())
	batch.AddADVEntry(entry)
	file.AddBatch(batch)
	file.ADVControl = mockADVFileControl()
	return file
}

// writeWithTinyBuffer writes file through a 1-byte buffer so a failing destination
// is noticed on the exact record (or line ending, or padding line) being written.
func writeWithTinyBuffer(file *File, dst *limitedWriter, lineEnding string) error {
	w := NewWriterWithOpts(dst, &WriteOpts{LineEnding: lineEnding})
	w.BypassValidation = true
	w.w = bufio.NewWriterSize(dst, 1)
	return w.Write(file)
}

func TestWriter_FailsPartwayThrough(t *testing.T) {
	files := map[string]func() *File{
		"every domestic and IAT record": failureTestFileWithEveryRecord,
		"ADV file":                      failureTestADVFile,
		"valid PPD file":                func() *File { return mockFilePPD(t) },
	}
	for name, makeFile := range files {
		for _, lineEnding := range []string{"\n", "\r\n"} {
			full := &limitedWriter{limit: 1 << 20}
			require.NoError(t, writeWithTinyBuffer(makeFile(), full, lineEnding), name)
			expected := full.buf.Bytes()
			require.Zero(t, len(expected)%((RecordLength+len(lineEnding))*10), "%s: output must be padded to a block of 10 lines", name)

			// Cut the destination off at every single byte position.
			for limit := 0; limit < len(expected); limit++ {
				dst := &limitedWriter{limit: limit}
				err := writeWithTinyBuffer(makeFile(), dst, lineEnding)

				require.ErrorIs(t, err, errDestinationFull, "%s: failure at byte %d was not reported", name, limit)
				require.Equal(t, string(expected[:limit]), dst.buf.String(), "%s: unexpected output before failure at byte %d", name, limit)
				require.Zero(t, dst.writesAfterFail, "%s: writer kept writing after failure at byte %d", name, limit)
			}
		}
	}
}

func TestWriter_FailureWithDefaultBuffer(t *testing.T) {
	// A file larger than the default 4KB buffer, so the writer flushes mid-file.
	file := NewFile().SetHeader(mockFileHeader())
	batch := NewBatchPPD(mockBatchPPDHeader())
	for i := 0; i < 100; i++ {
		batch.AddEntry(mockEntryDetail())
	}
	file.AddBatch(batch)

	full := &bytes.Buffer{}
	w := NewWriter(full)
	w.BypassValidation = true
	require.NoError(t, w.Write(file))
	require.Greater(t, full.Len(), 4096)

	for _, limit := range []int{0, 1000, 5000, full.Len() - 1} {
		dst := &limitedWriter{limit: limit}
		w := NewWriter(dst)
		w.BypassValidation = true

		require.ErrorIs(t, w.Write(file), errDestinationFull, "failure at byte %d was not reported", limit)
		require.Equal(t, string(full.Bytes()[:limit]), dst.buf.String())
		require.Zero(t, dst.writesAfterFail)
	}
}

func TestWriter_FlushNilWriter(t *testing.T) {
	var w *Writer
	require.EqualError(t, w.Flush(), "nil writer")
	require.EqualError(t, (&Writer{}).Flush(), "nil writer")
}

func TestWriter_FlushReportsFailure(t *testing.T) {
	dst := &limitedWriter{limit: 0}
	w := NewWriter(dst)
	require.NoError(t, w.writeLine(&FileHeader{}))
	require.ErrorIs(t, w.Flush(), errDestinationFull)
}

func TestNewWriterWithOpts_LineEnding(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriterWithOpts(&buf, &WriteOpts{LineEnding: "\r\n"})
	require.Equal(t, "\r\n", w.LineEnding)
	require.NoError(t, w.Write(mockFilePPD(t)))
	require.Contains(t, buf.String(), "\r\n")

	require.Equal(t, "\n", NewWriterWithOpts(&buf, nil).LineEnding)
	require.Equal(t, "\n", NewWriterWithOpts(&buf, &WriteOpts{}).LineEnding)
}
