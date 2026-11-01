package library

// 索引持久化（SQLite）：library.db 保存素材/项目/文件夹/项目集的索引。
//
// 设计原则与旧版 library.json 一致——索引只是「可重建的缓存」：
//   - 真实数据仍在 projects/<id>/project.json 与 assets/ 里，
//     索引库损坏时可由 Recover() 从目录结构恢复。
//   - 每个实体按 JSON 存一行（data 列），全量写入在单个事务内完成，
//     结构体字段增减无需迁移表结构。
//   - 首次启动若发现旧版 library.json，会自动迁移进 SQLite，
//     并把旧文件改名为 library.json.migrated 保留。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，Windows 下无需 CGO
)

const indexSchema = `
CREATE TABLE IF NOT EXISTS assets      (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS projects    (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS folders     (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS collections (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS meta        (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// openSQLite 打开（必要时创建）索引数据库并确保表结构存在。
func openSQLite(path string) (*sql.DB, error) {
	dsn := filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("library: 打开索引数据库失败：%w", err)
	}
	// 单连接串行化访问，避免多连接写锁竞争。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(indexSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("library: 初始化索引数据库失败：%w", err)
	}
	return db, nil
}

func (l *Library) dbPath() string { return filepath.Join(l.base, "library.db") }

// loadIndex 从 SQLite 载入索引；库为空时尝试从旧版 library.json 迁移。
func (l *Library) loadIndex() error {
	var total int
	err := l.db.QueryRow(
		`SELECT (SELECT COUNT(*) FROM assets) + (SELECT COUNT(*) FROM projects) +
		      (SELECT COUNT(*) FROM folders) + (SELECT COUNT(*) FROM collections)`,
	).Scan(&total)
	if err != nil {
		return fmt.Errorf("library: 读取索引数据库失败：%w", err)
	}
	if total == 0 {
		migrated, err := l.migrateFromJSON()
		if err != nil {
			return err
		}
		if migrated {
			return l.persistIndexLocked()
		}
		return nil
	}

	if err := l.scanTable("assets", func(raw []byte) error {
		var a Asset
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if a.ID != "" {
			l.index.Assets[a.ID] = &a
		}
		return nil
	}); err != nil {
		return err
	}
	if err := l.scanTable("projects", func(raw []byte) error {
		var p Project
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if p.ID != "" {
			l.index.Projects[p.ID] = &p
		}
		return nil
	}); err != nil {
		return err
	}
	if err := l.scanTable("folders", func(raw []byte) error {
		var f Folder
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		if f.ID != "" {
			l.index.Folders[f.ID] = &f
		}
		return nil
	}); err != nil {
		return err
	}
	return l.scanTable("collections", func(raw []byte) error {
		var c Collection
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if c.ID != "" {
			l.index.Collections[c.ID] = &c
		}
		return nil
	})
}

// scanTable 遍历某张表的 data 列。
func (l *Library) scanTable(table string, fn func([]byte) error) error {
	rows, err := l.db.Query("SELECT data FROM " + table)
	if err != nil {
		return fmt.Errorf("library: 读取索引数据库失败：%w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("library: 读取索引数据库失败：%w", err)
		}
		if err := fn(raw); err != nil {
			return fmt.Errorf("library: 解析索引数据失败：%w", err)
		}
	}
	return rows.Err()
}

// migrateFromJSON 把旧版 library.json 导入内存索引。
// 返回是否发生了迁移；旧文件损坏时忽略（后续由 Recover 自愈）。
func (l *Library) migrateFromJSON() (bool, error) {
	path := filepath.Join(l.base, "library.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, nil // 不存在或读不了：视为没有旧索引
	}
	var snap Index
	if err := json.Unmarshal(raw, &snap); err != nil {
		return false, nil
	}
	if len(snap.Assets) == 0 && len(snap.Projects) == 0 &&
		len(snap.Folders) == 0 && len(snap.Collections) == 0 {
		return false, nil
	}
	for id, a := range snap.Assets {
		if a != nil && a.ID != "" {
			l.index.Assets[id] = a
		}
	}
	for id, p := range snap.Projects {
		if p != nil && p.ID != "" {
			l.index.Projects[id] = p
		}
	}
	for id, f := range snap.Folders {
		if f != nil && f.ID != "" {
			l.index.Folders[id] = f
		}
	}
	for id, c := range snap.Collections {
		if c != nil && c.ID != "" {
			l.index.Collections[id] = c
		}
	}
	// 改名保留旧文件，确保迁移只发生一次。
	_ = os.Rename(path, path+".migrated")
	return true, nil
}

// persistIndexLocked 把内存索引全量写入 SQLite（单事务，幂等覆盖）。
func (l *Library) persistIndexLocked() error {
	l.mu.RLock()
	snapshot := l.index
	l.mu.RUnlock()

	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	return l.withTx(func(tx *sql.Tx) error {
		for table, rows := range map[string]map[string]any{
			"assets":      toAnyMap(snapshot.Assets),
			"projects":    toAnyMap(snapshot.Projects),
			"folders":     toAnyMap(snapshot.Folders),
			"collections": toAnyMap(snapshot.Collections),
		} {
			if _, err := tx.Exec("DELETE FROM " + table); err != nil {
				return err
			}
			for id, v := range rows {
				raw, err := json.Marshal(v)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(
					"INSERT INTO "+table+"(id,data) VALUES(?,?)", id, string(raw)); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(
			`INSERT INTO meta(key,value) VALUES('updatedAt',?)
			   ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			time.Now().Format(time.RFC3339))
		return err
	})
}

func toAnyMap[T any](m map[string]T) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// withTx 在单个事务中执行 fn，失败时回滚。
func (l *Library) withTx(fn func(*sql.Tx) error) error {
	tx, err := l.db.Begin()
	if err != nil {
		return fmt.Errorf("library: 写索引数据库失败：%w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("library: 写索引数据库失败：%w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("library: 写索引数据库失败：%w", err)
	}
	return nil
}

// Close 关闭索引数据库。
func (l *Library) Close() error {
	if l.db == nil {
		return nil
	}
	return l.db.Close()
}
