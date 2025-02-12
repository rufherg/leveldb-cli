// Copyright (c) 2021-2024 cions
// Licensed under the MIT License. See LICENSE for details.

package main

import (
	"bytes"
	"fmt"
	"github.com/cions/leveldb-cli/indexeddb"
	"github.com/fatih/color"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/comparer"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
	"github.com/urfave/cli/v2"
	"github.com/vmihailenco/msgpack/v5"
	"io"
	"os"
	"path"
	"regexp"
)

var leveldbFilenamePattern = regexp.MustCompile(`\A(?:LOCK|LOG(?:\.old)?|CURRENT(?:\.bak|\.\d+)?|MANIFEST-\d+|\d+\.(?:ldb|log|sst|tmp))\z`)

type entry struct {
	Key, Value []byte
}

func getComparer(c *cli.Context) comparer.Comparer {
	if c.Bool("indexeddb") {
		return indexeddb.Comparer
	}
	return comparer.DefaultComparer
}

func getArg(c *cli.Context, n int) ([]byte, error) {
	arg := []byte(c.Args().Get(n))
	if c.Bool("base64") {
		return decodeBase64(arg)
	} else if c.Bool("raw") {
		return arg, nil
	} else {
		return unescape(arg)
	}
}

func hasKeyRange(c *cli.Context) bool {
	flagNames := []string{
		"start",
		"start-raw",
		"start-base64",
		"end",
		"end-raw",
		"end-base64",
		"prefix",
		"prefix-raw",
		"prefix-base64",
	}
	for _, flagName := range flagNames {
		if c.IsSet(flagName) {
			return true
		}
	}
	return false
}

func getKeyRange(c *cli.Context) (*util.Range, error) {
	if c.IsSet("prefix-base64") {
		prefix, err := decodeBase64([]byte(c.String("prefix-base64")))
		if err != nil {
			return nil, fmt.Errorf("option --prefix-base64: %w", err)
		}
		if c.Bool("indexeddb") {
			return indexeddb.Prefix(prefix), nil
		}
		return util.BytesPrefix(prefix), nil
	}
	if c.IsSet("prefix-raw") {
		prefix := []byte(c.String("prefix-raw"))
		if c.Bool("indexeddb") {
			return indexeddb.Prefix(prefix), nil
		}
		return util.BytesPrefix(prefix), nil
	}
	if c.IsSet("prefix") {
		prefix, err := unescape([]byte(c.String("prefix")))
		if err != nil {
			return nil, fmt.Errorf("option --prefix: %w", err)
		}
		if c.Bool("indexeddb") {
			return indexeddb.Prefix(prefix), nil
		}
		return util.BytesPrefix(prefix), nil
	}

	slice := &util.Range{}

	if c.IsSet("start-base64") {
		start, err := decodeBase64([]byte(c.String("start-base64")))
		if err != nil {
			return nil, fmt.Errorf("option --start-base64: %w", err)
		}
		slice.Start = start
	} else if c.IsSet("start-raw") {
		slice.Start = []byte(c.String("start-raw"))
	} else if c.IsSet("start") {
		start, err := unescape([]byte(c.String("start")))
		if err != nil {
			return nil, fmt.Errorf("option --start: %w", err)
		}
		slice.Start = start
	}

	if c.IsSet("end-base64") {
		end, err := decodeBase64([]byte(c.String("end-base64")))
		if err != nil {
			return nil, fmt.Errorf("option --end-base64: %w", err)
		}
		slice.Limit = end
	} else if c.IsSet("end-raw") {
		slice.Limit = []byte(c.String("end-raw"))
	} else if c.IsSet("end") {
		end, err := unescape([]byte(c.String("end")))
		if err != nil {
			return nil, fmt.Errorf("option --end: %w", err)
		}
		slice.Limit = end
	}

	return slice, nil
}

type matcher interface {
	Match(key []byte) bool
}

type constMatcher bool

func (m constMatcher) Match(key []byte) bool {
	return bool(m)
}

type literalMatcher [][]byte

func (m literalMatcher) Match(key []byte) bool {
	for _, pattern := range m {
		if bytes.Equal(pattern, key) {
			return true
		}
	}
	return false
}

func newLiteralMatcher(keys ...[]byte) literalMatcher {
	return keys
}

type regexpMatcher []*regexp.Regexp

func (m regexpMatcher) Match(key []byte) bool {
	for _, pattern := range m {
		if pattern.Match(key) {
			return true
		}
	}
	return false
}

func newRegexpMatcher(patterns ...string) (regexpMatcher, error) {
	matcher := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		matcher = append(matcher, re)
	}
	return matcher, nil
}

func initCmd(c *cli.Context) error {
	db, err := leveldb.OpenFile(c.String("dbpath"), &opt.Options{
		Comparer:     getComparer(c),
		ErrorIfExist: true,
	})
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	return nil
}

func getCmd(c *cli.Context) error {
	if c.NArg() < 1 {
		cli.ShowSubcommandHelpAndExit(c, 2)
	}

	key, err := getArg(c, 0)
	if err != nil {
		return err
	}

	db, err := leveldb.OpenFile(c.String("dbpath"), &opt.Options{
		Comparer:       getComparer(c),
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	value, err := db.Get(key, nil)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(value); err != nil {
		return err
	}

	if err := db.Close(); err != nil {
		return err
	}

	return nil
}

func putCmd(c *cli.Context) error {
	if c.NArg() < 1 {
		cli.ShowSubcommandHelpAndExit(c, 2)
	}

	key, err := getArg(c, 0)
	if err != nil {
		return err
	}

	var value []byte
	if c.NArg() < 2 {
		value, err = io.ReadAll(os.Stdin)
	} else {
		value, err = getArg(c, 1)
	}
	if err != nil {
		return err
	}

	db, err := leveldb.OpenFile(c.String("dbpath"), &opt.Options{
		Comparer:       getComparer(c),
		ErrorIfMissing: true,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Put(key, value, nil); err != nil {
		return err
	}

	if err := db.Close(); err != nil {
		return err
	}

	return nil
}

func deleteCmd(c *cli.Context) error {
	if !hasKeyRange(c) && c.NArg() == 0 {
		cli.ShowSubcommandHelpAndExit(c, 2)
	}

	slice, err := getKeyRange(c)
	if err != nil {
		return err
	}
	inverted := c.Bool("invert-match")
	dryRun := c.Bool("dry-run")
	keywriter := newPrettyPrinter(color.Output).SetQuoting(true)

	var m matcher
	if c.NArg() == 0 {
		m = constMatcher(true)
	} else if c.Bool("regexp") {
		m, err = newRegexpMatcher(c.Args().Slice()...)
		if err != nil {
			return err
		}
	} else {
		keys := make([][]byte, c.NArg())
		for i := range c.NArg() {
			key, err := getArg(c, i)
			if err != nil {
				return err
			}
			keys = append(keys, key)
		}
		m = newLiteralMatcher(keys...)
	}

	db, err := leveldb.OpenFile(c.String("dbpath"), &opt.Options{
		Comparer:       getComparer(c),
		ErrorIfMissing: true,
		ReadOnly:       dryRun,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := db.GetSnapshot()
	if err != nil {
		return err
	}
	defer s.Release()

	batch := new(leveldb.Batch)

	iter := s.NewIterator(slice, nil)
	defer iter.Release()
	for iter.Next() {
		if m.Match(iter.Key()) != inverted {
			if dryRun {
				fmt.Print("Would delete ")
				keywriter.Write(iter.Key())
				fmt.Println()
			} else {
				batch.Delete(iter.Key())
			}
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	iter.Release()
	s.Release()

	if !dryRun {
		if err := db.Write(batch, nil); err != nil {
			return err
		}
	}

	if err := db.Close(); err != nil {
		return err
	}

	return nil
}

func keysCmd(c *cli.Context) error {
	var w io.Writer
	if c.Bool("base64") {
		w = newBase64Writer(os.Stdout)
	} else if c.Bool("raw") {
		w = os.Stdout
	} else {
		w = newPrettyPrinter(os.Stdout)
	}

	slice, err := getKeyRange(c)
	if err != nil {
		return err
	}

	db, err := leveldb.OpenFile(c.String("dbpath"), &opt.Options{
		Comparer:       getComparer(c),
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := db.GetSnapshot()
	if err != nil {
		return err
	}
	defer s.Release()

	iter := s.NewIterator(slice, nil)
	defer iter.Release()
	for iter.Next() {
		if _, err := w.Write(iter.Key()); err != nil {
			return err
		}
		if _, err := os.Stdout.WriteString("\n"); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	iter.Release()
	s.Release()
	if err := db.Close(); err != nil {
		return err
	}

	return nil
}

func showCmd(c *cli.Context) error {
	var kw, vw io.Writer
	if c.Bool("base64") {
		kw = newBase64Writer(os.Stdout)
		vw = newBase64Writer(os.Stdout)
	} else if c.Bool("raw") {
		kw = os.Stdout
		vw = os.Stdout
	} else {
		kw = newPrettyPrinter(color.Output).SetQuoting(true)
		vw = newPrettyPrinter(color.Output).
			SetQuoting(true).
			SetTruncate(!c.Bool("no-truncate")).
			SetParseJSON(!c.Bool("no-json"))
	}

	slice, err := getKeyRange(c)
	if err != nil {
		return err
	}

	db, err := leveldb.OpenFile(c.String("dbpath"), &opt.Options{
		Comparer:       getComparer(c),
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := db.GetSnapshot()
	if err != nil {
		return err
	}
	defer s.Release()

	iter := s.NewIterator(slice, nil)
	defer iter.Release()
	for iter.Next() {
		if _, err := kw.Write(iter.Key()); err != nil {
			return err
		}
		if _, err := os.Stdout.WriteString(": "); err != nil {
			return err
		}
		if _, err := vw.Write(iter.Value()); err != nil {
			return err
		}
		if _, err := os.Stdout.WriteString("\n"); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	iter.Release()
	s.Release()
	if err := db.Close(); err != nil {
		return err
	}

	return nil
}

func dumpDB(dbpath string, cmp comparer.Comparer, w io.Writer, pretty bool) error {
	db, err := leveldb.OpenFile(dbpath, &opt.Options{
		Comparer:       cmp,
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := db.GetSnapshot()
	if err != nil {
		return err
	}
	defer s.Release()

	var entries []entry

	iter := s.NewIterator(nil, nil)
	defer iter.Release()
	for iter.Next() {
		entries = append(entries, entry{
			Key:   bytes.Clone(iter.Key()),
			Value: bytes.Clone(iter.Value()),
		})
	}
	if err := iter.Error(); err != nil {
		return err
	}

	iter.Release()
	s.Release()
	if err := db.Close(); err != nil {
		return err
	}

	enc := msgpack.NewEncoder(w)
	enc.UseCompactInts(true)
	if err := enc.EncodeMapLen(len(entries)); err != nil {
		return err
	}
	for _, entry := range entries {
		if pretty {
			re, _ := regexp.Compile(`[\x00-\x1f\x7e-\xfd]`)
			if len(entry.Key) > 7 && len(entry.Value) > 2 {
				key := bytes.ReplaceAll(entry.Key, []byte{0x40, 0xff, 0xff}, []byte{})
				key = bytes.ReplaceAll(key, []byte{0xff, 0x14, 0xff}, []byte{})

				if _, err := w.Write(re.ReplaceAll(key, []byte(""))); err != nil {
					return err
				}
				//if err := enc.EncodeBytes(re.ReplaceAll(key, []byte(""))); err != nil {
				//	return err
				//}
			}
			if len(entry.Value) > 14 {
				value := bytes.ReplaceAll(entry.Value, []byte{0x40, 0xff, 0xff}, []byte{})
				value = bytes.ReplaceAll(value, []byte{0x77, 0x42, 0x7b}, []byte{})

				if _, err := w.Write(re.ReplaceAll(value, []byte(""))); err != nil {
					return err
				}
				//if err := enc.EncodeBytes(re.ReplaceAll(value, []byte(""))); err != nil {
				//	return err
				//}
				_, _ = w.Write([]byte{0x0a})
			}
		} else {
			if err := enc.EncodeBytes(entry.Key); err != nil {
				return err
			}
			if err := enc.EncodeBytes(entry.Value); err != nil {
				return err
			}
		}
	}

	return nil
}

func loadDB(dbpath string, cmp comparer.Comparer, r io.Reader) error {
	dec := msgpack.NewDecoder(r)

	nentries, err := dec.DecodeMapLen()
	if err != nil {
		return err
	}

	entries := make([]entry, nentries)
	for i := 0; i < nentries; i++ {
		key, err := dec.DecodeBytes()
		if err != nil {
			return err
		}
		value, err := dec.DecodeBytes()
		if err != nil {
			return err
		}
		entries[i].Key = key
		entries[i].Value = value
	}

	db, err := leveldb.OpenFile(dbpath, &opt.Options{
		Comparer: cmp,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	batch := new(leveldb.Batch)
	for _, entry := range entries {
		batch.Put(entry.Key, entry.Value)
	}
	if err := db.Write(batch, nil); err != nil {
		return err
	}

	if err := db.Close(); err != nil {
		return err
	}

	return nil
}

func destroyDB(dbpath string, dryRun bool) error {
	dir, err := os.Open(dbpath)
	if err != nil {
		return err
	}
	defer dir.Close()

	names, err := dir.Readdirnames(0)
	if err != nil {
		return err
	}

	for _, filename := range names {
		if !leveldbFilenamePattern.MatchString(filename) {
			continue
		}
		target := path.Join(dbpath, filename)
		if dryRun {
			fmt.Printf("Would remove %s\n", target)
			continue
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	}

	if err := dir.Close(); err != nil {
		return err
	}

	return nil
}

func dumpCmd(c *cli.Context) error {
	var w io.Writer = os.Stdout
	if c.NArg() >= 1 && c.Args().Get(0) != "-" {
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if c.Bool("no-clobber") {
			flags |= os.O_EXCL
		}
		fh, err := os.OpenFile(c.Args().Get(0), flags, 0o666)
		if err != nil {
			return err
		}
		defer fh.Close()
		w = fh
	}

	return dumpDB(c.String("dbpath"), getComparer(c), w, c.Bool("pretty"))
}

func loadCmd(c *cli.Context) error {
	var r io.Reader = os.Stdin
	if c.NArg() >= 1 && c.Args().Get(0) != "-" {
		fh, err := os.Open(c.Args().Get(0))
		if err != nil {
			return err
		}
		defer fh.Close()
		r = fh
	}

	return loadDB(c.String("dbpath"), getComparer(c), r)
}

func repairCmd(c *cli.Context) (err error) {
	db, err := leveldb.RecoverFile(c.String("dbpath"), nil)
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	return nil
}

func compactCmd(c *cli.Context) error {
	dbpath := c.String("dbpath")
	cmp := getComparer(c)
	bakfile := path.Join(dbpath, "leveldb.bak")

	bak, err := os.OpenFile(bakfile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer bak.Close()

	if err := dumpDB(dbpath, cmp, bak, false); err != nil {
		bak.Close()
		os.Remove(bakfile)
		return err
	}
	if _, err := bak.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := bak.Sync(); err != nil {
		return err
	}
	if err := destroyDB(dbpath, false); err != nil {
		return err
	}
	if err := loadDB(dbpath, cmp, bak); err != nil {
		return err
	}
	if err := bak.Close(); err != nil {
		return err
	}
	if err := os.Remove(bakfile); err != nil {
		return err
	}

	return nil
}

func destroyCmd(c *cli.Context) error {
	return destroyDB(c.String("dbpath"), c.Bool("dry-run"))
}
