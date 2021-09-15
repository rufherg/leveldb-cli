package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"regexp"

	"github.com/fatih/color"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/urfave/cli/v2"
	"github.com/vmihailenco/msgpack/v5"
)

var leveldbFilenamePattern = regexp.MustCompile(`^(?:LOCK|LOG(?:\.old)?|CURRENT(?:\.bak|\.\d+)?|MANIFEST-\d+|\d+\.(?:ldb|log|sst|tmp))$`)

type entry struct {
	Key, Value []byte
}

func getAllEntries(dbpath string) ([]entry, error) {
	opts := &opt.Options{ErrorIfMissing: true, ReadOnly: true}
	db, err := leveldb.OpenFile(dbpath, opts)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	s, err := db.GetSnapshot()
	if err != nil {
		return nil, err
	}

	var entries []entry

	iter := s.NewIterator(nil, nil)
	for iter.Next() {
		key := make([]byte, len(iter.Key()))
		copy(key, iter.Key())
		value := make([]byte, len(iter.Value()))
		copy(value, iter.Value())
		entries = append(entries, entry{Key: key, Value: value})
	}
	iter.Release()
	if err := iter.Error(); err != nil {
		return nil, err
	}

	return entries, nil
}

func initCmd(c *cli.Context) error {
	opts := &opt.Options{ErrorIfExist: true}
	db, err := leveldb.OpenFile(c.String("dbpath"), opts)
	if err != nil {
		return err
	}
	return db.Close()
}

func getCmd(c *cli.Context) error {
	if c.NArg() < 1 {
		cli.ShowCommandHelpAndExit(c, "get", 2)
	}

	var err error
	key := []byte(c.Args().Get(0))
	if c.Bool("base64") {
		key, err = decodeBase64(key)
	} else if !c.Bool("raw") {
		key, err = unescape(key)
	}
	if err != nil {
		return err
	}

	opts := &opt.Options{ErrorIfMissing: true, ReadOnly: true}
	db, err := leveldb.OpenFile(c.String("dbpath"), opts)
	if err != nil {
		return err
	}
	defer db.Close()

	value, err := db.Get(key, nil)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(value)
	return err
}

func putCmd(c *cli.Context) error {
	if c.NArg() < 1 {
		cli.ShowCommandHelpAndExit(c, "put", 2)
	}

	var err error

	key := []byte(c.Args().Get(0))
	if c.Bool("base64") {
		key, err = decodeBase64(key)
	} else if !c.Bool("raw") {
		key, err = unescape(key)
	}
	if err != nil {
		return err
	}

	var value []byte
	if c.NArg() == 1 {
		value, err = io.ReadAll(os.Stdin)
	} else {
		value = []byte(c.Args().Get(1))
		if c.Bool("base64") {
			value, err = decodeBase64(value)
		} else if !c.Bool("raw") {
			value, err = unescape(value)
		}
	}
	if err != nil {
		return err
	}

	opts := &opt.Options{ErrorIfMissing: true}
	db, err := leveldb.OpenFile(c.String("dbpath"), opts)
	if err != nil {
		return err
	}
	defer db.Close()

	return db.Put(key, value, nil)
}

func deleteCmd(c *cli.Context) error {
	if c.NArg() < 1 {
		cli.ShowCommandHelpAndExit(c, "delete", 2)
	}

	var err error
	key := []byte(c.Args().Get(0))
	if c.Bool("base64") {
		key, err = decodeBase64(key)
	} else if !c.Bool("raw") {
		key, err = unescape(key)
	}
	if err != nil {
		return err
	}

	opts := &opt.Options{ErrorIfMissing: true}
	db, err := leveldb.OpenFile(c.String("dbpath"), opts)
	if err != nil {
		return err
	}
	defer db.Close()

	return db.Delete(key, nil)
}

func keysCmd(c *cli.Context) error {
	var w io.Writer = os.Stdout
	if c.Bool("base64") {
		w = newBase64Writer(os.Stdout)
	} else if !c.Bool("raw") {
		w = newPrettyPrinter(color.Output)
	}

	entries, err := getAllEntries(c.String("dbpath"))
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if _, err := w.Write(entry.Key); err != nil {
			return err
		}
		if _, err := fmt.Println(); err != nil {
			return err
		}
	}

	return nil
}

func showCmd(c *cli.Context) error {
	var kw, vw io.Writer = os.Stdout, os.Stdout
	if c.Bool("base64") {
		kw = newBase64Writer(os.Stdout)
		vw = newBase64Writer(os.Stdout)
	} else if !c.Bool("raw") {
		kw = newPrettyPrinter(color.Output).SetQuoting(true)
		vw = newPrettyPrinter(color.Output).
			SetQuoting(true).
			SetTruncate(!c.Bool("no-truncate")).
			SetParseJSON(!c.Bool("no-json"))
	}

	entries, err := getAllEntries(c.String("dbpath"))
	if err != nil {
		return err
	}

	for _, entry := range entries {
		kw.Write(entry.Key)
		fmt.Print(": ")
		vw.Write(entry.Value)
		fmt.Println()
	}

	return nil
}

func dumpDB(dbpath string, w io.Writer) error {
	entries, err := getAllEntries(dbpath)
	if err != nil {
		return err
	}

	enc := msgpack.NewEncoder(w)
	if err := enc.EncodeMapLen(len(entries)); err != nil {
		return err
	}

	for _, entry := range entries {
		if err := enc.EncodeBytes(entry.Key); err != nil {
			return err
		}
		if err := enc.EncodeBytes(entry.Value); err != nil {
			return err
		}
	}

	return nil
}

func loadDB(dbpath string, r io.Reader) error {
	dec := msgpack.NewDecoder(r)

	nentries, err := dec.DecodeMapLen()
	if err != nil {
		return err
	}

	entries := make([]entry, 0, nentries)
	for i := 0; i < nentries; i++ {
		key, err := dec.DecodeBytes()
		if err != nil {
			return err
		}
		value, err := dec.DecodeBytes()
		if err != nil {
			return err
		}
		entries = append(entries, entry{Key: key, Value: value})
	}

	db, err := leveldb.OpenFile(dbpath, nil)
	if err != nil {
		return err
	}
	defer db.Close()

	batch := new(leveldb.Batch)
	for _, entry := range entries {
		batch.Put(entry.Key, entry.Value)
	}
	return db.Write(batch, nil)
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

	return nil
}

func compactCmd(c *cli.Context) error {
	dbpath := c.String("dbpath")
	bakfile := path.Join(dbpath, "leveldb.bak")

	bak, err := os.OpenFile(bakfile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer bak.Close()

	if err := dumpDB(dbpath, bak); err != nil {
		return err
	}

	if _, err := bak.Seek(0, os.SEEK_SET); err != nil {
		return err
	}

	if err := destroyDB(dbpath, false); err != nil {
		return err
	}

	if err := loadDB(dbpath, bak); err != nil {
		return err
	}

	bak.Close()
	return os.Remove(bakfile)
}
