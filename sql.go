package imapsql

import "github.com/pkg/errors"

func (b *Backend) initSchema() error {
	var err error
	_, err = b.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL NOT NULL PRIMARY KEY AUTOINCREMENT,
			username VARCHAR(255) NOT NULL UNIQUE,
			msgsizelimit INTEGER DEFAULT NULL,
			password VARCHAR(255) DEFAULT NULL,
			password_salt VARCHAR(255) DEFAULT NULL
		)`)
	if err != nil {
		return errors.Wrap(err, "create table users")
	}
	_, err = b.db.Exec(`
		CREATE TABLE IF NOT EXISTS mboxes (
			id BIGSERIAL NOT NULL PRIMARY KEY AUTOINCREMENT,
			uid INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name VARCHAR(255) NOT NULL,
			sub INTEGER NOT NULL DEFAULT 0,
			mark INTEGER NOT NULL DEFAULT 0,
			msgsizelimit INTEGER DEFAULT NULL,
			uidnext INTEGER NOT NULL DEFAULT 1,
			uidvalidity BIGINT NOT NULL,

			UNIQUE(uid, name)
		)`)
	if err != nil {
		return errors.Wrap(err, "create table mboxes")
	}
	_, err = b.db.Exec(`
		CREATE TABLE IF NOT EXISTS msgs (
			mboxId BIGINT NOT NULL REFERENCES mboxes(id) ON DELETE CASCADE,
			msgId BIGINT NOT NULL,
			date BIGINT NOT NULL,
			headerLen INTEGER NOT NULL,
			header LONGTEXT,
			bodyLen INTEGER NOT NULL,
			body LONGTEXT NOT NULL,
			mark INTEGER NOT NULL DEFAULT 0,

			PRIMARY KEY(mboxId, msgId)
		)`)
	if err != nil {
		return errors.Wrap(err, "create table msgs")
	}
	_, err = b.db.Exec(`
		CREATE TABLE IF NOT EXISTS flags (
			mboxId BIGINT NOT NULL,
			msgId BIGINT NOT NULL,
			flag VARCHAR(255) NOT NULL,

			FOREIGN KEY (mboxId, msgId) REFERENCES msgs(mboxId, msgId) ON DELETE CASCADE,
			UNIQUE (mboxId, msgId, flag)
		)`)
	if err != nil {
		return errors.Wrap(err, "create table flags")
	}
	return nil
}

func (b *Backend) prepareStmts() error {
	var err error

	b.userCreds, err = b.db.Prepare(`
		SELECT id, password, password_salt
		FROM users
		WHERE username = ?`)
	if err != nil {
		return errors.Wrap(err, "userCreds prep")
	}
	b.listUsers, err = b.db.Prepare(`
		SELECT id, username
		FROM users`)
	if err != nil {
		return errors.Wrap(err, "listUsers prep")
	}
	b.addUser, err = b.db.Prepare(`
		INSERT INTO users(username, password, password_salt)
		VALUES (?, ?, ?)`)
	if err != nil {
		return errors.Wrap(err, "addUser prep")
	}
	b.delUser, err = b.db.Prepare(`
		DELETE FROM users
		WHERE username = ?`)
	if err != nil {
		return errors.Wrap(err, "addUser prep")
	}
	b.setUserPass, err = b.db.Prepare(`
		UPDATE users
		SET password = ?, password_salt = ?
		WHERE username = ?`)
	if err != nil {
		return errors.Wrap(err, "addUser prep")
	}
	b.listMboxes, err = b.db.Prepare(`
		SELECT id, name
		FROM mboxes
		WHERE uid = ?`)
	if err != nil {
		return errors.Wrap(err, "listMboxes prep")
	}
	b.listSubbedMboxes, err = b.db.Prepare(`
		SELECT id, name
		FROM mboxes
		WHERE uid = ? AND sub = 1`)
	if err != nil {
		return errors.Wrap(err, "listSubbedMboxes prep")
	}
	b.createMbox, err = b.db.Prepare(`
		INSERT INTO mboxes(uid, name, uidvalidity)
		VALUES (?, ?, ?)`)
	if err != nil {
		return errors.Wrap(err, "createMbox prep")
	}
	b.createMboxExistsOk, err = b.db.Prepare(`
		INSERT INTO mboxes(uid, name, uidvalidity)
		VALUES (?, ?, ?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return errors.Wrap(err, "createMboxExistsOk prep")
	}
	b.deleteMbox, err = b.db.Prepare(`
		DELETE FROM mboxes
		WHERE uid = ? AND name = ?`)
	if err != nil {
		return errors.Wrap(err, "deleteMbox prep")
	}
	b.renameMbox, err = b.db.Prepare(`
		UPDATE mboxes SET name = ?
		WHERE uid = ? AND name = ?`)
	if err != nil {
		return errors.Wrap(err, "renameMbox prep")
	}
	if b.db.driver == "mysql" {
		b.renameMboxChilds, err = b.db.Prepare(`
		UPDATE mboxes SET name = concat(?, substr(name, ?+1))
		WHERE name LIKE ? AND uid = ?`)
	} else {
		b.renameMboxChilds, err = b.db.Prepare(`
		UPDATE mboxes SET name = ? || substr(name, ?+1)
		WHERE name LIKE ? AND uid = ?`)
	}
	if err != nil {
		return errors.Wrap(err, "renameMboxChilds prep")
	}
	b.getMboxMark, err = b.db.Prepare(`
		SELECT mark FROM mboxes
		WHERE uid = ? AND name = ?`)
	if err != nil {
		return errors.Wrap(err, "getMboxMark prep")
	}
	b.setSubbed, err = b.db.Prepare(`
		UPDATE mboxes SET sub = ?
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "setSubbed prep")
	}
	b.hasChildren, err = b.db.Prepare(`
		SELECT count(*)
		FROM mboxes
		WHERE name LIKE ? AND uid = ?`)
	if err != nil {
		return errors.Wrap(err, "hasChildren prep")
	}
	b.uidNext, err = b.db.Prepare(`
		SELECT uidnext
		FROM mboxes
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "uidNext prep")
	}
	b.addUidNext, err = b.db.Prepare(`
		UPDATE mboxes
		SET uidnext = uidnext + ?
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "addUidNext prep")
	}
	b.uidValidity, err = b.db.Prepare(`
		SELECT uidvalidity
		FROM mboxes
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "uidvalidity prep")
	}
	b.msgsCount, err = b.db.Prepare(`
		SELECT count(*)
		FROM msgs
		WHERE mboxId = ?`)
	if err != nil {
		return errors.Wrap(err, "msgsCount prep")
	}
	b.recentCount, err = b.db.Prepare(`
		SELECT count(*)
		FROM flags
		WHERE mboxId = ? AND flag = '\Recent'`)
	if err != nil {
		return errors.Wrap(err, "recentCount prep")
	}
	// TODO: This query is kinda expensive, consider moving
	// flags with special semantics (Recent, Seen, Deleted) to
	// msgs table as columns.
	if b.db.mysql57 {
		// MySQL 5.7 doesn't have row_number() function.
		b.firstUnseenSeqNum, err = b.db.Prepare(`
		SELECT coalesce(rownr, 0)
		FROM (
			SELECT (@rownum := @rownum + 1) AS rownr, msgId
			FROM msgs, (SELECT @rownum := 0) counter
			WHERE mboxId = ?
			ORDER BY msgId
		) seqnum
		WHERE msgId NOT IN (
			SELECT msgId
			FROM flags
			WHERE mboxId = ?
			AND flag = '\Seen'
		)`)
	} else {
		b.firstUnseenSeqNum, err = b.db.Prepare(`
			SELECT rownr
			FROM (
				SELECT row_number() OVER (ORDER BY msgId) AS rownr, msgId
				FROM msgs
				WHERE mboxId = ?
			) seqnum
			WHERE msgId NOT IN (
				SELECT msgId
				FROM flags
				WHERE mboxId = ?
				AND flag = '\Seen'
			)`)
	}
	if err != nil {
		return errors.Wrap(err, "firstUnseenSeqNum prep")
	}
	if b.db.mysql57 {
		b.deletedSeqnums, err = b.db.Prepare(`
			SELECT seqnum
			FROM (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
			) seqnums
			WHERE msgId IN (
				SELECT msgId
				FROM flags
				WHERE mboxId = ?
				AND flag = '\Deleted'
			)
			ORDER BY seqnum DESC`)
	} else {
		b.deletedSeqnums, err = b.db.Prepare(`
			SELECT seqnum
			FROM (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId
				FROM msgs
				WHERE mboxId = ?
			) seqnums
			WHERE msgId IN (
				SELECT msgId
				FROM flags
				WHERE mboxId = ?
				AND flag = '\Deleted'
			)
			ORDER BY seqnum DESC`)
	}
	if err != nil {
		return errors.Wrap(err, "deletedSeqnums prep")
	}
	b.expungeMbox, err = b.db.Prepare(`
		DELETE FROM msgs
		WHERE mboxId = ? AND msgId IN (
			SELECT msgId
			FROM flags
			WHERE mboxId = ?
			AND flag = '\Deleted'
		)`)
	if err != nil {
		return errors.Wrap(err, "expungeMbox prep")
	}
	b.mboxId, err = b.db.Prepare(`
		SELECT id FROM mboxes
		WHERE uid = ?
		AND name = ?`)
	if err != nil {
		return errors.Wrap(err, "mboxId prep")
	}
	b.addMsg, err = b.db.Prepare(`
		INSERT INTO msgs(mboxId, msgId, date, headerLen, header, bodyLen, body)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return errors.Wrap(err, "addMsg prep")
	}
	if b.db.mysql57 {
		b.copyMsgsUid, err = b.db.Prepare(`
			INSERT INTO msgs
			SELECT ? AS mboxId, (
				SELECT uidnext - 1
				FROM mboxes
				WHERE id = ?
			) + (@rownum := @rownum + 1), date, headerLen, header, bodyLen, body, 0 AS mark
			FROM msgs, (SELECT @rownum := 0) counter
			WHERE mboxId = ? AND msgId BETWEEN ? AND ?
			ORDER BY msgId`)
	} else {
		b.copyMsgsUid, err = b.db.Prepare(`
			INSERT INTO msgs
			SELECT ? AS mboxId, (
				SELECT uidnext - 1
				FROM mboxes
				WHERE id = ?
			) + row_number() OVER (ORDER BY msgId), date, headerLen, header, bodyLen, body, 0 AS mark
			FROM msgs
			WHERE mboxId = ? AND msgId BETWEEN ? AND ?`)
	}
	if err != nil {
		return errors.Wrap(err, "copyMsgsUid prep")
	}
	if b.db.mysql57 {
		b.copyMsgFlagsUid, err = b.db.Prepare(`
			INSERT INTO flags
			SELECT ?, new_msgId AS msgId, flag
			FROM flags
			INNER JOIN (
				SELECT (
					SELECT uidnext - 1
					FROM mboxes
					WHERE id = ?
				) + (@rownum := @rownum + 1) AS new_msgId, msgId, mboxId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
				AND msgId BETWEEN ? AND ?
				ORDER BY msgId
			) map ON map.msgId = flags.msgId
			AND map.mboxId = flags.mboxId`)
	} else {
		b.copyMsgFlagsUid, err = b.db.Prepare(`
			INSERT INTO flags
			SELECT ?, new_msgId AS msgId, flag
			FROM flags
			INNER JOIN (
				SELECT (
					SELECT uidnext - 1
					FROM mboxes
					WHERE id = ?
				) + row_number() OVER (ORDER BY msgId) AS new_msgId, msgId, mboxId
				FROM msgs
				WHERE mboxId = ?
				AND msgId BETWEEN ? AND ?
			) map ON map.msgId = flags.msgId
			AND map.mboxId = flags.mboxId`)
	}
	if err != nil {
		return errors.Wrap(err, "copyMsgFlagsUid prep")
	}
	if b.db.mysql57 {
		b.copyMsgsSeq, err = b.db.Prepare(`
			INSERT INTO msgs
			SELECT ? AS mboxId, (
				SELECT uidnext - 1
				FROM mboxes
				WHERE id = ?
			) + (@rownum := @rownum + 1), date, headerLen, header, bodyLen, body, 0 AS mark
			FROM (
				SELECT msgId, date, headerLen, header, bodyLen, body
				FROM msgs
				WHERE mboxId = ?
				ORDER BY msgId
				LIMIT ? OFFSET ?
			) subset, (SELECT @rownum := 0) counter
			ORDER BY msgId`)
	} else {
		b.copyMsgsSeq, err = b.db.Prepare(`
			INSERT INTO msgs
			SELECT ? AS mboxId, (
				SELECT uidnext - 1
				FROM mboxes
				WHERE id = ?
			) + row_number() OVER (ORDER BY msgId), date, headerLen, header, bodyLen, body, 0 AS mark
			FROM (
				SELECT msgId, date, headerLen, header, bodyLen, body
				FROM msgs
				WHERE mboxId = ?
				ORDER BY msgId
				LIMIT ? OFFSET ?
			) subset`)
	}
	if err != nil {
		return errors.Wrap(err, "copyMsgsSeq prep")
	}
	if b.db.mysql57 {
		b.copyMsgFlagsSeq, err = b.db.Prepare(`
			INSERT INTO flags
			SELECT ?, new_msgId AS msgId, flag
			FROM flags
			INNER JOIN (
				SELECT (
					SELECT uidnext - 1
					FROM mboxes
					WHERE id = ?
				) + (@rownum := @rownum + 1) AS new_msgId, msgId, mboxId
				FROM (
					SELECT msgId, mboxId
					FROM msgs
					WHERE mboxId = ?
					ORDER BY msgId
					LIMIT ? OFFSET ?
				) subset, (SELECT @rownum := 0) counter
			) map ON map.msgId = flags.msgId
			AND map.mboxId = flags.mboxId`)
	} else {
		b.copyMsgFlagsSeq, err = b.db.Prepare(`
			INSERT INTO flags
			SELECT ?, new_msgId AS msgId, flag
			FROM flags
			INNER JOIN (
				SELECT (
					SELECT uidnext - 1
					FROM mboxes
					WHERE id = ?
				) + row_number() OVER (ORDER BY msgId) AS new_msgId, msgId, mboxId
				FROM (
					SELECT msgId, mboxId
					FROM msgs
					WHERE mboxId = ?
					ORDER BY msgId
					LIMIT ? OFFSET ?
				) subset
			) map ON map.msgId = flags.msgId
			AND map.mboxId = flags.mboxId`)
	}
	if err != nil {
		return errors.Wrap(err, "copyMsgFlagsSeq prep")
	}
	if b.db.mysql57 {
		b.getMsgsNoBodyUid, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, NULL, bodyLen, NULL, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId, mboxId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND msgs.msgId BETWEEN ? AND ?
			GROUP BY msgs.mboxId, msgs.msgId, seqnum
			ORDER BY msgs.msgId`)
	} else {
		b.getMsgsNoBodyUid, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, NULL, bodyLen, NULL, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId, mboxId
				FROM msgs
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND msgs.msgId BETWEEN ? AND ?
			GROUP BY msgs.mboxId, msgs.msgId, seqnum
			ORDER BY msgs.msgId`)
	}
	if err != nil {
		return errors.Wrap(err, "getMsgsNoBodyUid prep")
	}
	if b.db.mysql57 {
		b.getMsgsBodyUid, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, header, bodyLen, body, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId, mboxId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND msgs.msgId BETWEEN ? AND ?
			GROUP BY seqnum, msgs.mboxId, msgs.msgId
			ORDER BY msgs.msgId`)
	} else {
		b.getMsgsBodyUid, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, header, bodyLen, body, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId, mboxId
				FROM msgs
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND msgs.msgId BETWEEN ? AND ?
			GROUP BY seqnum, msgs.mboxId, msgs.msgId
			ORDER BY msgs.msgId`)
	}
	if err != nil {
		return errors.Wrap(err, "getMsgsBodyUid prep")
	}
	if b.db.mysql57 {
		b.getMsgsNoBodySeq, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, NULL, bodyLen, NULL, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId, mboxId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
				ORDER BY msgId
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND seqnum BETWEEN ? AND ?
			GROUP BY seqnum, msgs.mboxId, msgs.msgId
			ORDER BY msgs.msgId`)
	} else {
		b.getMsgsNoBodySeq, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, NULL, bodyLen, NULL, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId, mboxId
				FROM msgs
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND seqnum BETWEEN ? AND ?
			GROUP BY seqnum, msgs.mboxId, msgs.msgId
			ORDER BY msgs.msgId`)
	}
	if err != nil {
		return errors.Wrap(err, "getMsgsNoBodySeq prep")
	}
	if b.db.mysql57 {
		b.getMsgsBodySeq, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, header, bodyLen, body, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId, mboxId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
				ORDER BY msgId
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId
			WHERE msgs.mboxId = ? AND seqnum BETWEEN ? AND ? AND msgs.mboxId = map.mboxId
			GROUP BY seqnum, msgs.mboxId, msgs.msgId
			ORDER BY msgs.msgId`)
	} else {
		b.getMsgsBodySeq, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, date, headerLen, header, bodyLen, body, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId, mboxId
				FROM msgs
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId
			WHERE msgs.mboxId = ? AND seqnum BETWEEN ? AND ? AND msgs.mboxId = map.mboxId
			GROUP BY seqnum, msgs.mboxId, msgs.msgId
			ORDER BY msgs.msgId`)
	}
	if err != nil {
		return errors.Wrap(err, "getMsgsBodySeq prep")
	}
	b.massClearFlagsUid, err = b.db.Prepare(`
		DELETE FROM flags
		WHERE mboxId = ?
		AND msgId BETWEEN ? AND ?
		AND flag != '\Recent'`)
	if err != nil {
		return errors.Wrap(err, "massClearFlagsUid prep")
	}
	if b.db.mysql57 {
		b.massClearFlagsSeq, err = b.db.Prepare(`
			DELETE FROM flags
			WHERE mboxId = ?
			AND msgId IN (
				SELECT msgId
				FROM (
					SELECT (@rownum := @rownum + 1) AS seqnum, msgId
					FROM msgs, (SELECT @rownum := 0) counter
					WHERE mboxId = ?
				) seq
				WHERE seqnum BETWEEN ? AND ?
			)
			AND flag != '\Recent'`)
	} else {
		b.massClearFlagsSeq, err = b.db.Prepare(`
			DELETE FROM flags
			WHERE mboxId = ?
			AND msgId IN (
				SELECT msgId
				FROM (
					SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId
					FROM msgs
					WHERE mboxId = ?
				) seq
				WHERE seqnum BETWEEN ? AND ?
			)
			AND flag != '\Recent'`)
	}
	if err != nil {
		return errors.Wrap(err, "massClearFlagsSeq prep")
	}

	b.addRecentToLast, err = b.db.Prepare(`
		INSERT INTO flags
		SELECT ? AS mboxId, msgId, '\Recent'
		FROM (SELECT msgId FROM msgs WHERE mboxId = ? ORDER BY msgId DESC LIMIT ?) targets
		ON CONFLICT DO NOTHING
		`)
	if err != nil {
		return errors.Wrap(err, "addRecenttoLast prep")
	}

	b.markUid, err = b.db.Prepare(`
		UPDATE msgs
		SET mark = 1
		WHERE mboxId = ?
		AND msgId BETWEEN ? AND ?`)
	if err != nil {
		return errors.Wrap(err, "delMsgsUid prep")
	}
	if b.db.mysql57 {
		b.markSeq, err = b.db.Prepare(`
			UPDATE msgs
			SET mark = 1
			WHERE mboxId = ?
			AND msgId IN (
				SELECT msgId
				FROM (
					SELECT (@rownum := @rownum + 1) AS seqnum, msgId
					FROM msgs, (SELECT @rownum := 0) counter
					WHERE mboxId = ?
				) seq
				WHERE seqnum BETWEEN ? AND ?
			)`)
	} else {
		b.markSeq, err = b.db.Prepare(`
			UPDATE msgs
			SET mark = 1
			WHERE mboxId = ?
			AND msgId IN (
				SELECT msgId
				FROM (
					SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId
					FROM msgs
					WHERE mboxId = ?
				) seq
				WHERE seqnum BETWEEN ? AND ?
			)`)
	}
	if err != nil {
		return errors.Wrap(err, "delMsgsSeq prep")
	}
	b.delMarked, err = b.db.Prepare(`
		DELETE FROM msgs
		WHERE mark = 1`)
	if err != nil {
		return errors.Wrap(err, "delMarked prep")
	}
	if b.db.mysql57 {
		b.markedSeqnums, err = b.db.Prepare(`
			SELECT seqnum
			FROM (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId, mark
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
			) seqnums
			WHERE mark = 1
			ORDER BY seqnum DESC`)
	} else {
		b.markedSeqnums, err = b.db.Prepare(`
			SELECT seqnum
			FROM (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId, mark
				FROM msgs
				WHERE mboxId = ?
			) seqnums
			WHERE mark = 1
			ORDER BY seqnum DESC`)
	}
	if err != nil {
		return errors.Wrap(err, "deletedSeqnums prep")
	}

	b.setUserMsgSizeLimit, err = b.db.Prepare(`
		UPDATE users
		SET msgsizelimit = ?
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "setUserMsgSizeLimit prep")
	}
	b.userMsgSizeLimit, err = b.db.Prepare(`
		SELECT msgsizelimit
		FROM users
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "userMsgSizeLimit prep")
	}
	b.setMboxMsgSizeLimit, err = b.db.Prepare(`
		UPDATE mboxes
		SET msgsizelimit = ?
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "setUserMsgSizeLimit prep")
	}
	b.mboxMsgSizeLimit, err = b.db.Prepare(`
		SELECT msgsizelimit
		FROM mboxes
		WHERE id = ?`)
	if err != nil {
		return errors.Wrap(err, "userMsgSizeLimit prep")
	}

	if b.db.mysql57 {
		b.msgFlagsUid, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId, mboxId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND msgs.msgId BETWEEN ? AND ?
			GROUP BY msgs.mboxId, msgs.msgId, seqnum
			ORDER BY seqnum DESC`)
	} else {
		b.msgFlagsUid, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId, mboxId
				FROM msgs
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND msgs.msgId BETWEEN ? AND ?
			GROUP BY msgs.mboxId, msgs.msgId, seqnum
			ORDER BY seqnum DESC`)
	}
	if err != nil {
		return errors.Wrap(err, "msgFlagsUid prep")
	}
	if b.db.mysql57 {
		b.msgFlagsSeq, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT (@rownum := @rownum + 1) AS seqnum, msgId, mboxId
				FROM msgs, (SELECT @rownum := 0) counter
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND seqnum BETWEEN ? AND ?
			GROUP BY msgs.mboxId, msgs.msgId, seqnum
			ORDER BY seqnum DESC`)
	} else {
		b.msgFlagsSeq, err = b.db.Prepare(`
			SELECT seqnum, msgs.msgId, coalesce(` + b.groupConcatFn("flag", "{") + `, '')
			FROM msgs
			INNER JOIN (
				SELECT row_number() OVER (ORDER BY msgId) AS seqnum, msgId, mboxId
				FROM msgs
				WHERE mboxId = ?
			) map
			ON map.msgId = msgs.msgId
			LEFT JOIN flags
			ON flags.msgId = msgs.msgId AND flags.mboxId = map.mboxId AND msgs.mboxId = flags.mboxId
			WHERE msgs.mboxId = ? AND seqnum BETWEEN ? AND ?
			GROUP BY msgs.mboxId, msgs.msgId, seqnum
			ORDER BY seqnum DESC`)
	}
	if err != nil {
		return errors.Wrap(err, "msgFlagsSeq prep")
	}

	return nil
}