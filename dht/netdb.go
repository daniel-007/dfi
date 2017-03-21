/*
 *  Zif
 *  Copyright (C) 2017 Zif LTD
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU Affero General Public License as published
 *  by the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU Affero General Public License for more details.

 *  You should have received a copy of the GNU Affero General Public License
 *  along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */
package dht

import (
	"database/sql"
	"encoding/json"
	"io/ioutil"

	_ "github.com/mattn/go-sqlite3"
	log "github.com/sirupsen/logrus"
)

const (
	BucketSize = 20
)

type NetDB struct {
	table [][]Address
	addr  Address
	conn  *sql.DB

	stmtInsertEntry      *sql.Stmt
	stmtInsertFtsEntry   *sql.Stmt
	stmtEntryLen         *sql.Stmt
	stmtQueryAddress     *sql.Stmt
	stmtInsertSeed       *sql.Stmt
	stmtQueryIdByAddress *sql.Stmt
	stmtUpdateEntry      *sql.Stmt
	stmtQuerySeeds       *sql.Stmt
	stmtQuerySeeding     *sql.Stmt
	stmtQueryLatest      *sql.Stmt
	stmtSearchPeer       *sql.Stmt
}

func NewNetDB(addr Address, path string) (*NetDB, error) {
	var err error

	ret := &NetDB{}
	ret.addr = addr

	// One bucket of addresses per bit in an address
	// At the time of writing, uses roughly 64KB of memory
	ret.table = make([][]Address, AddressBinarySize*8)

	// allocate each bucket
	for n, _ := range ret.table {
		ret.table[n] = make([]Address, 0, BucketSize)
	}

	ret.conn, err = sql.Open("sqlite3", path)

	if err != nil {
		return nil, err
	}

	// don't bother preparing these, they are only used at startup

	// create the entries table first, it is most important
	_, err = ret.conn.Exec(sqlCreateEntriesTable)
	if err != nil {
		return nil, err
	}

	// store seed lists
	_, err = ret.conn.Exec(sqlCreateSeedsTable)
	if err != nil {
		return nil, err
	}

	// full text search
	_, err = ret.conn.Exec(sqlCreateFtsTable)
	if err != nil {
		return nil, err
	}

	// speed up entry lookups
	_, err = ret.conn.Exec(sqlIndexAddresses)
	if err != nil {
		return nil, err
	}

	// prepare all the SQL we will be needing
	ret.stmtInsertEntry, err = ret.conn.Prepare(sqlInsertEntry)
	if err != nil {
		return nil, err
	}

	ret.stmtInsertFtsEntry, err = ret.conn.Prepare(sqlInsertFtsEntry)
	if err != nil {
		return nil, err
	}

	ret.stmtQueryAddress, err = ret.conn.Prepare(sqlQueryAddress)
	if err != nil {
		return nil, err
	}

	ret.stmtInsertSeed, err = ret.conn.Prepare(sqlInsertSeed)
	if err != nil {
		return nil, err
	}

	ret.stmtQueryIdByAddress, err = ret.conn.Prepare(sqlQueryIdByAddress)
	if err != nil {
		return nil, err
	}

	ret.stmtUpdateEntry, err = ret.conn.Prepare(sqlUpdateEntry)
	if err != nil {
		return nil, err
	}

	ret.stmtQuerySeeds, err = ret.conn.Prepare(sqlQuerySeeds)
	if err != nil {
		return nil, err
	}

	ret.stmtQuerySeeding, err = ret.conn.Prepare(sqlQuerySeeding)
	if err != nil {
		return nil, err
	}

	ret.stmtEntryLen, err = ret.conn.Prepare(sqlEntryLen)
	if err != nil {
		return nil, err
	}

	ret.stmtQueryLatest, err = ret.conn.Prepare(sqlQueryLatest)
	if err != nil {
		return nil, err
	}

	ret.stmtSearchPeer, err = ret.conn.Prepare(sqlSearchEntries)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// Get the total size of the in-memory routing table
func (ndb *NetDB) TableLen() int {
	size := 0

	for _, i := range ndb.table {
		size += len(i)
	}

	return size
}

// Get the total number of entries we have stored
func (ndb *NetDB) Len() (int, error) {
	var length int

	row := ndb.stmtEntryLen.QueryRow()
	err := row.Scan(&length)

	if err != nil {
		return -1, err
	}

	return length, err
}

// Insert an address into the in memory routing table. Theere is no need to store
// any data along with it as this can be fetched from the DB.
func (ndb *NetDB) insertIntoTable(addr Address) {
	// Find the distance between the kv address and our own address, this is the
	// index in the table
	index := addr.Xor(&ndb.addr).LeadingZeroes()
	bucket := ndb.table[index]

	// there is capacity, insert at the front
	// search to see if it is already inserted

	found := -1

	for n, i := range bucket {
		if i.Equals(&addr) {
			found = n
			break
		}
	}

	// if it already exists, it first needs to be removed from it's old position
	if found != -1 {
		bucket = append(bucket[:found], bucket[found+1:]...)
	} else if len(bucket) == BucketSize {

		// remove the back of the bucket, this update will go at the front
		bucket = bucket[:len(bucket)-1]
	}

	bucket = append([]Address{addr}, bucket...)

	ndb.table[index] = bucket

	ndb.SaveTable("./data/table.dat")
}

// Returns updated, inserted. One should be zero.
func (ndb *NetDB) insertIntoDB(entry Entry) (int64, error) {

	addressString, err := entry.Address.String()

	if err != nil {
		return 0, err
	}

	// Insert the entry into the main entry table
	res, err := ndb.stmtInsertEntry.Exec(addressString, entry.Name, entry.Desc,
		entry.PublicAddress, entry.Port, entry.PublicKey,
		entry.Signature, entry.CollectionHash,
		entry.PostCount, len(entry.Seeds), len(entry.Seeding),
		entry.Updated, entry.Seen)

	if err != nil {
		return 0, err
	}

	affected, err := res.RowsAffected()

	if err != nil {
		return 0, err
	}

	if affected == 0 {
		return 0, nil
	}

	id, err := res.LastInsertId()

	if err != nil {
		return 0, err
	}

	res, err = ndb.stmtInsertFtsEntry.Exec(id, entry.Name, entry.Desc)

	return affected, err
}

func (ndb *NetDB) insertEntrySeeds(entry Entry) error {
	// if that is all ok, then we can register all the seeds in the seed table
	// fun thing about this table, it can be used to populate both Seeds and
	// Seeding :D
	// Also need to make sure to not insert duplicates. SQL constraints should
	// do that for me. Woop woop!

	// first, register all the seeds for peers we are a seed for
	for _, i := range entry.Seeding {
		peer := Address{Raw: i}

		// we are a seed for this peer
		err := ndb.InsertSeed(peer, entry.Address)

		if err != nil {
			return err
		}
	}

	// then register all of the seeds for the current peer!
	for _, i := range entry.Seeds {
		peer := Address{Raw: i}

		// the peer is a seed for us
		err := ndb.InsertSeed(entry.Address, peer)

		if err != nil {
			return err
		}
	}

	return nil
}

func (ndb *NetDB) InsertSeed(entry Address, seed Address) error {
	// First we need to map the addresses, which are essentially a network-wide
	// id, to an integer id which is local to our database.
	entryAddressString, err := entry.String()
	seedAddressString, err := seed.String()

	if err != nil {
		return err
	}

	entryIdRes := ndb.stmtQueryIdByAddress.QueryRow(entryAddressString)
	seedIdRes := ndb.stmtQueryIdByAddress.QueryRow(seedAddressString)

	entryId := -1
	seedId := -1

	err = entryIdRes.Scan(&entryId)
	if err != nil {
		return err
	}

	err = seedIdRes.Scan(&seedId)
	if err != nil {
		return err
	}

	// got the ids, so now insert them into the database!
	_, err = ndb.stmtInsertSeed.Exec(seedId, entryId)

	return err
}

// Inserts an entry into both the routing table and the database
// Returns number of affected entries and error
func (ndb *NetDB) Insert(entry Entry) (int64, error) {
	err := entry.Verify()

	if err != nil {
		return 0, err
	}

	log.WithField("peer", entry.Address.StringOr("")).Debug("Inserting into NetDB")

	ndb.insertIntoTable(entry.Address)

	// attempts to update, if this fails then the insert succeeds. Otherwise it
	// is updated and the insert fails
	affected, err := ndb.Update(entry)
	if err != nil {
		log.Error(err.Error())
		return 0, err
	}

	if affected > 0 {
		return affected, nil
	}

	affected, err = ndb.insertIntoDB(entry)
	if err != nil {
		log.Error(err.Error())
		return 0, err
	}

	return affected, ndb.insertEntrySeeds(entry)
}

func (ndb *NetDB) Update(entry Entry) (int64, error) {
	err := entry.Verify()

	if err != nil {
		return 0, err
	}

	addressString, err := entry.Address.String()

	if err != nil {
		return 0, err
	}

	res, err := ndb.stmtUpdateEntry.Exec(entry.Name, entry.Desc, entry.PublicAddress,
		entry.Port, entry.PublicKey, entry.Signature,
		entry.CollectionHash, entry.PostCount, len(entry.Seeds), len(entry.Seeding),
		entry.Updated, entry.Seen, addressString)

	if err == sql.ErrNoRows {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	affected, err := res.RowsAffected()

	return affected, err
}

// Returns the KeyValue if this node has the address, nil if not, and err otherwise
func (ndb *NetDB) Query(addr Address) (*Entry, int, error) {
	addressString, err := addr.String()

	if err != nil {
		return nil, -1, err
	}

	ret := Entry{}
	row := ndb.stmtQueryAddress.QueryRow(addressString)

	id := 0
	seedCount := 0
	seedingCount := 0
	address := ""

	err = row.Scan(&id, &address, &ret.Name, &ret.Desc, &ret.PublicAddress,
		&ret.Port, &ret.PublicKey, &ret.Signature, &ret.CollectionHash,
		&ret.PostCount, &seedCount, &seedingCount, &ret.Updated, &ret.Seen)

	if err == sql.ErrNoRows {
		return nil, -1, nil
	}

	if err != nil {
		return nil, -1, err
	}

	decoded, err := DecodeAddress(address)

	if err != nil {
		return nil, -1, err
	}

	ret.Address.Raw = make([]byte, len(decoded.Raw))
	copy(ret.Address.Raw, decoded.Raw)

	err = ndb.addSeedToEntry(&ret, seedCount, seedingCount, id)
	if err != nil {
		return nil, 0, err
	}

	// resinsert into the table, this keeps popular things easy to access
	// TODO: Store some sort of "lastQueried" in the database, then we have
	// even more data on how popular something is.
	// TODO: Make sure I'm not storing too much in the database :P
	ndb.insertIntoTable(ret.Address)
	return &ret, id, nil
}

func (ndb *NetDB) addSeedToEntry(e *Entry, seedCount, seedingCount, id int) error {
	e.Seeding = make([][]byte, 0, seedingCount)
	e.Seeds = make([][]byte, 0, seedCount)

	// now that all the slices for seeds/seeding are there, we need to popular them
	// we also already have the id, which is nice
	seeds, err := ndb.querySeeds(id)
	if err != nil {
		return err
	}

	seeding, err := ndb.querySeeding(id)
	if err != nil {
		return err
	}

	for _, i := range seeds {
		e.Seeds = append(e.Seeds, i.Raw)
	}

	for _, i := range seeding {
		e.Seeding = append(e.Seeding, i.Raw)
	}

	return nil
}

// fetch the seeds for an entry, given the entry and its id
func (ndb *NetDB) querySeeds(id int) ([]Address, error) {
	ret := make([]Address, 0)

	log.Debug("Querying seeds from netdb")
	seeds, err := ndb.stmtQuerySeeds.Query(id)

	if err != nil {
		return nil, err
	}

	// we should now have all the addresses we need, loop through, decode,
	// and stick them into the seeder list! Still unsure if they should be
	// stored in sqlite encoded, it does make debugging easier however.
	address := ""
	for seeds.Next() {
		err = seeds.Scan(&address)

		if err != nil {
			return nil, err
		}

		// decode the address
		addr, err := DecodeAddress(address)
		if err != nil {
			return nil, err
		}

		ret = append(ret, addr)
	}

	return ret, nil
}

// fetch what an entry is seeding
func (ndb *NetDB) querySeeding(id int) ([]Address, error) {
	ret := make([]Address, 0)

	log.Debug("Querying seeds from netdb")
	seeds, err := ndb.stmtQuerySeeding.Query(id)

	if err != nil {
		return nil, err
	}

	address := ""
	for seeds.Next() {
		err = seeds.Scan(&address)

		if err != nil {
			return nil, err
		}

		// decode the address
		addr, err := DecodeAddress(address)
		if err != nil {
			return nil, err
		}

		ret = append(ret, addr)
	}

	return ret, nil
}

// Fetch the seeds for an entry, given its address
func (ndb *NetDB) QuerySeeds(addr Address) ([]Address, error) {
	// get the entry and ID
	_, id, err := ndb.Query(addr)

	if err != nil {
		return nil, err
	}

	addresses, err := ndb.querySeeds(id)

	return addresses, err

}

func (ndb *NetDB) QuerySeeding(addr Address) ([]Address, error) {
	// get the entry and ID
	_, id, err := ndb.Query(addr)

	if err != nil {
		return nil, err
	}

	return ndb.querySeeding(id)

}

func (ndb *NetDB) queryAddresses(as []Address) Entries {
	ret := make(Entries, 0, len(as))

	for _, i := range as {
		kv, _, err := ndb.Query(i)

		if err != nil {
			continue
		}

		ret = append(ret, kv)
	}

	return ret
}

func (ndb *NetDB) FindClosest(addr Address) (Entries, error) {
	// Find the distance between the kv address and our own address, this is the
	// index in the table
	index := addr.Xor(&ndb.addr).LeadingZeroes()
	bucket := ndb.table[index]

	if len(bucket) == BucketSize {
		return ndb.queryAddresses(bucket), nil
	}

	ret := make(Entries, 0, BucketSize)

	// Start with bucket, copy all across, then move left outwards checking all
	// other buckets.
	for i := 0; (index-i >= 0 || index+i <= len(addr.Raw)*8) &&
		len(ret) < BucketSize; i++ {

		if index-i >= 0 {
			bucket = ndb.table[index-i]

			for _, i := range bucket {
				if len(ret) >= BucketSize {
					break
				}

				kv, _, err := ndb.Query(i)

				if err != nil {
					continue
				}

				ret = append(ret, kv)
			}
		}

		if index+i < len(addr.Raw)*8 {
			bucket = ndb.table[index+i]

			for _, i := range bucket {
				if len(ret) >= BucketSize {
					break
				}

				kv, _, err := ndb.Query(i)

				if err != nil {
					continue
				}

				ret = append(ret, kv)
			}
		}

	}

	return ret, nil
}

func (ndb *NetDB) QueryLatest() ([]Entry, error) {
	ret := make([]Entry, 0, 20)
	entries, err := ndb.stmtQueryLatest.Query()

	if err != nil {
		return nil, err
	}

	for entries.Next() {
		e := Entry{}

		id := 0
		seedCount := 0
		seedingCount := 0
		address := ""

		err = entries.Scan(&id, &address, &e.Name, &e.Desc, &e.PublicAddress,
			&e.Port, &e.PublicKey, &e.Signature, &e.CollectionHash,
			&e.PostCount, &seedCount, &seedingCount, &e.Updated, &e.Seen)

		if err != nil {
			return nil, err
		}

		err = ndb.addSeedToEntry(&e, seedCount, seedingCount, id)
		if err != nil {
			return nil, err
		}

		ret = append(ret, e)
	}

	return ret, nil
}

func (ndb *NetDB) SearchPeer(name, desc string, page int) ([]Address, error) {
	ret := make([]Address, 0, 20)
	addresses, err := ndb.stmtSearchPeer.Query(name, desc, page, 25)

	if err != nil {
		return nil, err
	}

	for addresses.Next() {
		s := ""

		err = addresses.Scan(&s)

		if err != nil {
			return nil, err
		}

		a, err := DecodeAddress(s)

		if err != nil {
			return nil, err
		}

		ret = append(ret, a)
	}

	return ret, nil
}

func (ndb *NetDB) SaveTable(path string) {
	data, err := json.Marshal(ndb.table)

	if err != nil {
		log.Error(err.Error())
	}

	ioutil.WriteFile(path, data, 0644)

}

func (ndb *NetDB) LoadTable(path string) {
	raw, _ := ioutil.ReadFile(path)

	json.Unmarshal(raw, &ndb.table)
}
