package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// stmts is every statement this package runs, prepared once rather than
// compiled on each call.
//
// It is not a micro-optimisation and it is not free to skip: a cold walk of a
// large tree runs four of these per file, and SQLite parses and plans a
// statement every time one arrives as text. The pool closes them when it
// closes.
type stmts struct {
	nodeByIdent          *sql.Stmt
	nodeByIdentNoBtime   *sql.Stmt
	nodeIDByIdent        *sql.Stmt
	nodeIDByIdentNoBtime *sql.Stmt

	nodeIdentByID *sql.Stmt
	nodeRowByID   *sql.Stmt
	insertNode    *sql.Stmt
	moveNode      *sql.Stmt
	touchNode     *sql.Stmt
	renameNode    *sql.Stmt
	readDiretag   *sql.Stmt
	putDiretag    *sql.Stmt
	dirtyDiretag  *sql.Stmt
	bumpShareGen  *sql.Stmt
	readShareGen  *sql.Stmt
}

func prepare(ctx context.Context, db *sql.DB) (*stmts, error) {
	s := &stmts{}
	for _, p := range []struct {
		into **sql.Stmt
		text string
	}{
		{&s.nodeByIdent, sqlNodeByIdent},
		{&s.nodeByIdentNoBtime, sqlNodeByIdentNoBtime},
		{&s.nodeIDByIdent, sqlNodeIDByIdent},
		{&s.nodeIDByIdentNoBtime, sqlNodeIDByIdentNoBtime},
		{&s.nodeIdentByID, sqlNodeIdentByID},
		{&s.nodeRowByID, sqlNodeRowByID},
		{&s.insertNode, sqlInsertNode},
		{&s.moveNode, sqlMoveNode},
		{&s.touchNode, sqlTouchNode},
		{&s.renameNode, sqlRenameNode},
		{&s.readDiretag, sqlReadDiretag},
		{&s.putDiretag, sqlPutDiretag},
		{&s.dirtyDiretag, sqlDirtyDiretag},
		{&s.bumpShareGen, sqlBumpShareGen},
		{&s.readShareGen, sqlReadShareGen},
	} {
		st, err := db.PrepareContext(ctx, p.text)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("preparing a statement: %w", err), s.close())
		}
		*p.into = st
	}
	return s, nil
}

func (s *stmts) close() error {
	var err error
	for _, st := range []*sql.Stmt{
		s.nodeByIdent, s.nodeByIdentNoBtime, s.nodeIDByIdent, s.nodeIDByIdentNoBtime,
		s.nodeIdentByID, s.nodeRowByID,
		s.insertNode, s.moveNode, s.touchNode, s.renameNode,
		s.readDiretag, s.putDiretag, s.dirtyDiretag, s.bumpShareGen, s.readShareGen,
	} {
		if st != nil {
			err = errors.Join(err, st.Close())
		}
	}
	return err
}
