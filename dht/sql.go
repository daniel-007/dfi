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

/*
	This file stores all the SQL queries needed for the NetDB.
	It will also be used to prepare all SQL statements :)
*/

const (
	/*
		id             - primary key
		address        - the encoded zif address
		name           - the user-defined name of the node
		desc           - the user-defined description of the node
		publicAddress  - the user-defined address of the node on the internet/tor/etc
		port           - the port used to connect to the node
		publicKey      - the publicKey for the node, can be checked with the address
		signature      - this entry, signed
		collectionHash - the root hash of all the posts
		postCount      - the number of posts this node has
		seedCount      - the number of seeds this node has
		updated        - when this entry was last updated by the node, or another adding seeds
		seen           - when this node was last seen online

		Zif addresses are stored encoded mostly because it makes debugging *far*
		easier, at the code of some extra encoding and decoding.
	*/
	sqlCreateEntriesTable = `
			CREATE TABLE IF NOT EXISTS
				entry(
					id INTEGER PRIMARY KEY NOT NULL,
					address STRING(40) UNIQUE,
					name STRING(64) NOT NULL,
					desc STRING(256),
					publicAddress STRING(256) NOT NULL,
					port INT,
					publicKey BLOB(32) NOT NULL,
					signature BLOB(64),
					collectionHash BLOB(32),
					postCount INT,
					seedCount INT,
					seedingCount INT,
					updated INT,
					seen INT
				)
	`

	// Create the seeds table, using to link together seeds and the actual node
	// constraint should make sure we don't end up with duplicate seeds
	// TODO: Make sure the constraint is only one way. IE, allow both x,y and y,x
	// to exist.
	sqlCreateSeedsTable = `
		CREATE TABLE IF NOT EXISTS 
				seed(
					id INTEGER PRIMARY KEY NOT NULL,
					seed INTEGER NOT NULL,
					for INTEGER NOT NULL,
					UNIQUE(seed, for) ON CONFLICT REPLACE
				)
	`
	// The full text search virtual table, allowing for the search of a node by
	// description and name.
	sqlCreateFtsTable = `
			CREATE VIRTUAL TABLE IF NOT EXISTS
				ftsEntry using fts4(
					content="entry",
					name,
					desc
				)
	`
	sqlUpdateEntry = `
			UPDATE entry SET 
				name=?,
				desc=?,
				publicAddress=?,
				port=?,
				publicKey=?,
				signature=?,
				collectionHash=?,
				postCount=?,
				seedCount=?,
				seedingCount=?,
				updated=?,
				seen=?
			WHERE address=?
	`

	sqlInsertEntry = `
			INSERT OR IGNORE INTO entry (
				address,
				name,
				desc,
				publicAddress,
				port,
				publicKey,
				signature,
				collectionHash,
				postCount,
				seedCount,
				seedingCount,
				updated,
				seen
			)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	sqlInsertSeed = `
			INSERT OR IGNORE INTO seed (
				seed,
				for
			) VALUES (?, ?)
	`

	sqlInsertFtsEntry = `
			INSERT OR IGNORE INTO ftsEntry (
				docid,
				name,
				desc
			) VALUES(?, ?, ?)
	`

	// We need an index on addresses, as nodes wll be fetched by index really
	// quite often. Most of the time actually! It's probably a good idea to cache
	// in RAM for n seconds.
	sqlIndexAddresses = `
			CREATE INDEX IF NOT EXISTS
				addressIndex ON entry(address)
	`

	sqlQueryAddress = `
		SELECT * FROM entry WHERE address=?
	`

	sqlQueryIdByAddress = `
		SELECT id FROM entry WHERE address=?
	`

	// Get all the seeders for a given address
	sqlQuerySeeds = `
		SELECT entry.address FROM entry
			JOIN seed
				ON entry.id = seed.seed
			WHERE seed.for = ?
	`

	// pretty much the opposite of the above, get a list of addresses that the
	// peer is seeding
	sqlQuerySeeding = `
		SELECT entry.address FROM entry
			JOIN seed
				ON entry.id = seed.for
			WHERE seed.seed = ?
	`

	sqlEntryLen = `
		SELECT MAX(id) FROM entry
	`

	sqlQueryLatest = `
		SELECT * FROM entry ORDER BY id DESC LIMIT 20
	`

	sqlSearchEntries = `
		SELECT entry.address FROM entry 
			WHERE entry.id=(
				SELECT docid FROM ftsEntry WHERE name MATCH ? OR desc MATCH ?
			)
		LIMIT ?,?
	`
)
