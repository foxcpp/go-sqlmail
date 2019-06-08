package imapsql

import (
	"bufio"
	"database/sql"
	"strings"
	"time"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/backendutil"
	"github.com/emersion/go-message/textproto"
)

func (m *Mailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	if searchOnlyWithFlags(criteria) {
		if criteria.Not == nil && criteria.Or == nil && criteria.WithFlags == nil && criteria.WithoutFlags == nil {
			return m.allSearch(uid)
		}

		return m.flagSearch(uid, criteria.WithFlags, criteria.WithoutFlags)
	}

	needBody := searchNeedsBody(criteria)
	noSeqNum := noSeqNumNeeded(criteria)
	var rows *sql.Rows
	var err error
	if needBody {
		if noSeqNum && uid {
			rows, err = m.parent.searchFetchNoSeq.Query(m.id)
		} else {
			rows, err = m.parent.searchFetch.Query(m.id, m.id)
		}
	} else {
		if noSeqNum && uid {
			rows, err = m.parent.searchFetchNoBodyNoSeq.Query(m.id)
		} else {
			rows, err = m.parent.searchFetchNoBody.Query(m.id, m.id)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []uint32
	for rows.Next() {
		var seqNum, msgId uint32
		var dateUnix int64
		var bodyLen int
		var headerBlob, bodyBlob []byte
		var flagStr string
		var extBodyKey sql.NullString

		if needBody {
			err = rows.Scan(&seqNum, &msgId, &dateUnix, &headerBlob, &bodyLen, &extBodyKey, &bodyBlob, &flagStr)
		} else {
			err = rows.Scan(&seqNum, &msgId, &dateUnix, &bodyLen, &flagStr)
		}
		if err != nil {
			return nil, err
		}

		flags := strings.Split(flagStr, flagsSep)
		if len(flags) == 1 && flags[0] == "" {
			flags = nil
		}

		var parsedHeader textproto.Header
		var bufferedBody *bufio.Reader
		if needBody {
			body, err := m.openBody(extBodyKey, headerBlob, bodyBlob)
			if err != nil {
				return nil, err
			}
			bufferedBody = bufio.NewReader(body)

			parsedHeader, err = textproto.ReadHeader(bufferedBody)
			if err != nil {
				return nil, err
			}
		} else {
			parsedHeader = textproto.Header{}
			bufferedBody = nil
		}

		matched, err := backendutil.Match(parsedHeader, bufferedBody, seqNum, msgId, time.Unix(dateUnix, 0), flags, criteria)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}

		if uid {
			res = append(res, msgId)
		} else {
			res = append(res, seqNum)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return res, nil
}

func searchNeedsBody(criteria *imap.SearchCriteria) bool {
	if criteria.Header != nil ||
		criteria.Body != nil ||
		criteria.Text != nil ||
		!criteria.SentSince.IsZero() ||
		!criteria.SentBefore.IsZero() ||
		criteria.Smaller != 0 ||
		criteria.Larger != 0 {

		return true
	}

	for _, crit := range criteria.Not {
		if searchNeedsBody(crit) {
			return true
		}
	}
	for _, crit := range criteria.Or {
		if searchNeedsBody(crit[0]) || searchNeedsBody(crit[1]) {
			return true
		}
	}

	return false
}

func searchOnlyWithFlags(criteria *imap.SearchCriteria) bool {
	if criteria.Header != nil ||
		criteria.Body != nil ||
		criteria.Text != nil ||
		!criteria.SentSince.IsZero() ||
		!criteria.SentBefore.IsZero() ||
		criteria.Smaller != 0 ||
		criteria.Uid != nil ||
		criteria.SeqNum != nil ||
		!criteria.Since.IsZero() ||
		!criteria.Before.IsZero() ||
		criteria.Larger != 0 ||
		criteria.Not != nil ||
		criteria.Or != nil {

		return false
	}

	return true
}

func noSeqNumNeeded(criteria *imap.SearchCriteria) bool {
	if criteria.SeqNum != nil {
		return false
	}

	for _, crit := range criteria.Not {
		if !noSeqNumNeeded(crit) {
			return false
		}
	}
	for _, crit := range criteria.Or {
		if !noSeqNumNeeded(crit[0]) || !noSeqNumNeeded(crit[1]) {
			return false
		}
	}

	return true
}

func (m *Mailbox) allSearch(uid bool) ([]uint32, error) {
	if !uid {
		row := m.parent.msgsCount.QueryRow(m.id)
		var count uint32
		if err := row.Scan(&count); err != nil {
			return nil, err
		}

		seqs := make([]uint32, 0, count)
		for i := uint32(1); i <= count; i++ {
			seqs = append(seqs, i)
		}
		return seqs, nil
	}

	rows, err := m.parent.listMsgUids.Query(m.id)
	if err != nil {
		return nil, err
	}

	var uids []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}

		uids = append(uids, uid)
	}
	return uids, nil
}

func (m *Mailbox) flagSearch(uid bool, withFlags, withoutFlags []string) ([]uint32, error) {
	stmt, err := m.getFlagSearchStmt(uid, withFlags, withoutFlags)
	if err != nil {
		return nil, err
	}

	args := m.buildFlagSearchQueryArgs(uid, withFlags, withoutFlags)
	rows, err := stmt.Query(args...)
	if err != nil {
		return nil, err
	}

	var res []uint32
	for rows.Next() {
		var id uint32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		res = append(res, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}
