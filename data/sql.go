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
package data

const sql_create_post_table string = `CREATE TABLE IF NOT EXISTS 
										post(
											id INTEGER PRIMARY KEY NOT NULL,
											info_hash STRING UNIQUE,
											title STRING NOT NULL,
											size INTEGER NOT NULL,
											file_count INTEGER NOT NULL,
											seeders INTEGER NOT NULL,
											leechers INTEGER NOT NULL,
											upload_date INTEGER NOT NULL,
											tags STRING,
											meta STRING
										)`

const sql_create_fts_post string = `CREATE VIRTUAL TABLE IF NOT EXISTS
									fts_post using fts4(
										content="post",
										title,
										seeders,
										leechers
									)`

const sql_create_upload_date_index string = `CREATE INDEX IF NOT EXISTS
											port_upload_date_index
											ON post(upload_date)`

const sql_insert_post string = `INSERT OR IGNORE INTO post(
									info_hash,
									title,
									size,
									file_count,
									seeders,
									leechers,
									upload_date,
									tags,
									meta
								) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`

const sql_attach_meta string = `UPDATE POST
								SET meta=?
								WHERE id=?`

const sql_generate_fts string = `INSERT OR IGNORE INTO fts_post(
								docid,
								title,
								seeders,
								leechers)
							SELECT id, title, seeders, leechers FROM post 
							WHERE id >= ?`

const sql_query_recent_post string = `SELECT 	 * FROM post
												 ORDER BY upload_date DESC
												 LIMIT ?,?`

const sql_query_popular_post string = ` SELECT * FROM(
													SELECT * FROM post 
													ORDER BY upload_date DESC
													LIMIT 10000
												)
												 ORDER BY seeders + leechers DESC
												 LIMIT ?,?`

const sql_query_post_id string = `SELECT 	 * FROM post
												 WHERE id = ?`

const sql_query_paged_post string = `SELECT 	 * FROM post
												 WHERE id > ?
												 LIMIT 0,?`

// Seeders are weighted, things with more seeders are better than things with
// more leechers, though both are important.
// (for one, seeders DO still upload, and are indicative of popularity)
const sql_search_post string = `SELECT docid FROM fts_post
									WHERE title MATCH ?
									ORDER BY ((seeders * 1.1) + leechers) DESC
									LIMIT ?,?`

const sql_suggest_posts string = `SELECT title FROM (
										SELECT * FROM post
										ORDER BY upload_date DESC
										LIMIT 100000
									)
									WHERE title LIKE ?
									ORDER BY (seeders * 1.1) + leechers DESC
									LIMIT 0,?`

const sql_count_post = `SELECT MAX(id) FROM post`

const sql_update_seed_leecth = `UPDATE post
								SET seeders=?
								WHERE id=?`

const sql_update_leechers = `UPDATE post
								SET leechers=?
								WHERE id=?`

const sql_update_seeders = `UPDATE post
								SET seeders=?
								WHERE id=?`
