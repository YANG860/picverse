// Package testdb 提供依赖真实 PostgreSQL 的集成测试基础设施（templateDB 方案）：
// 维护一个已迁移完毕的模板库 test_template，每个测试用例通过
// CREATE DATABASE ... TEMPLATE 克隆出独立数据库，用例结束后删除。
// 用例之间零共享，天然支持 t.Parallel()。
//
// 模板库由 scripts/dbinit 手动重建（迁移变更后需重跑）：
//
//	go run ./scripts/dbinit
//
// 默认连接串可用环境变量 TEST_PG_URL 覆盖（其中的库名仅用于定位服务器，实际测试库由本包克隆创建）：
//
//	TEST_PG_URL=postgres://user:pass@host:5432/test?sslmode=disable go test ./internal/media/v1/...
//
// 使用方式：
//
//	func TestXxx(t *testing.T) {
//	    db := testdb.Setup(t) // 克隆独立库 + 注册结束清理
//	    repo := v1.NewMediaRepo(db)
//	    ...
//	}
package testdb

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DefaultDSN 测试服务器默认连接串（指向远程 PostgreSQL，不走生产库）。
// 串中的库名不参与测试，仅作为服务器/凭据的默认来源。
const DefaultDSN = "postgres://postgres:postgres123@106.53.172.186:5432/test?sslmode=disable"

// TemplateDBName 已执行过全量迁移的模板库名，由 scripts/dbinit 重建维护。
const TemplateDBName = "test_template"

// scratchPrefix 每用例克隆库的命名前缀，用于区分模板库与业务库。
const scratchPrefix = "test_"

// dbNameRe 限制数据库名，避免拼接到 CREATE/DROP DATABASE 时出现注入。
var dbNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// scratchSeq 进程内克隆库计数器，与 pid、随机数共同保证并发唯一。
var scratchSeq atomic.Int64

// DSN 返回测试服务器连接串，优先使用 TEST_PG_URL。
// 注意：串中的库名不被测试使用，测试库由本包按用例克隆创建。
func DSN() string {
	if v := strings.TrimSpace(os.Getenv("TEST_PG_URL")); v != "" {
		return v
	}
	return DefaultDSN
}

// dsnFor 返回指向指定数据库的连接串（复用 DSN 的服务器与凭据）。
func dsnFor(name string) (string, error) {
	u, err := url.Parse(DSN())
	if err != nil {
		return "", fmt.Errorf("解析连接串失败: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// maintDSN 返回指向维护库(postgres)的连接串，用于 CREATE/DROP DATABASE。
func maintDSN() (string, error) {
	u, err := url.Parse(DSN())
	if err != nil {
		return "", fmt.Errorf("解析连接串失败: %w", err)
	}
	u.Path = "/postgres"
	return u.String(), nil
}

// openGorm 建立一个 gorm 连接（配置与生产一致，保证错误翻译行为相同）。
func openGorm(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// 远程测试库，限制连接数，避免测试并发时打满
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(2)
	if err := sqlDB.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// execOn 维护库上执行语句（CREATE/DROP DATABASE 等非事务 DDL）。
func execOn(maint *gorm.DB, stmt string) error {
	if err := maint.Exec(stmt).Error; err != nil {
		return fmt.Errorf("执行 %q 失败: %w", stmt, err)
	}
	return nil
}

// databaseExists 查询维护库上目标库是否存在。
func databaseExists(maint *gorm.DB, name string) (bool, error) {
	var exists bool
	if err := maint.Raw("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = ?)", name).Scan(&exists).Error; err != nil {
		return false, fmt.Errorf("查询数据库是否存在失败: %w", err)
	}
	return exists, nil
}

// dropDatabase 删除指定数据库（先终止残留连接，重试一次）。无守卫，仅限本包内部使用。
func dropDatabase(maint *gorm.DB, name string) error {
	terminate := fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", name)
	for range 2 {
		_ = maint.Exec(terminate).Error // 容忍无连接可终止的报错，DROP 的错误才是关键
		if err := execOn(maint, "DROP DATABASE "+name); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond) // 连接完全释放可能略有延迟
	}
	return fmt.Errorf("删除数据库 %s 失败（连接可能未释放）", name)
}

// dropDB 删除测试克隆库，带守卫：仅允许符合命名规范的库，且禁止误删模板库。
func dropDB(maint *gorm.DB, name string) error {
	if !dbNameRe.MatchString(name) {
		return fmt.Errorf("数据库名不合法（仅允许字母/数字/下划线）: %q", name)
	}
	if name == TemplateDBName {
		return fmt.Errorf("禁止删除模板库 %s", TemplateDBName)
	}
	return dropDatabase(maint, name)
}

// EnsureTemplate 重建模板库：DROP 旧的 test_template → CREATE → 执行 sql/*.sql 迁移。
// 仅由 scripts/dbinit 调用；迁移变更后需重跑，否则测试跑在旧 schema 上。
func EnsureTemplate() error {
	maintDSN, err := maintDSN()
	if err != nil {
		return err
	}
	maint, err := openGorm(maintDSN)
	if err != nil {
		return fmt.Errorf("连接维护库失败 (%s): %w", maintDSN, err)
	}
	sqlDB, err := maint.DB()
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := dropDatabase(maint, TemplateDBName); err != nil {
		return err
	}
	if err := execOn(maint, "CREATE DATABASE "+TemplateDBName); err != nil {
		return err
	}

	tdbDSN, err := dsnFor(TemplateDBName)
	if err != nil {
		return err
	}
	db, err := openGorm(tdbDSN)
	if err != nil {
		return fmt.Errorf("连接模板库失败 (%s): %w", tdbDSN, err)
	}
	tdbSQLDB, err := db.DB()
	if err != nil {
		return err
	}
	defer func() { _ = tdbSQLDB.Close() }()

	if err := MigrateDB(db); err != nil {
		return fmt.Errorf("迁移模板库失败: %w", err)
	}
	return nil
}

// OpenDB 按库名建立连接（不迁移、不清表），便于工具类调用方自行控制生命周期。
func OpenDB(name string) (*gorm.DB, error) {
	if !dbNameRe.MatchString(name) {
		return nil, fmt.Errorf("数据库名不合法（仅允许字母/数字/下划线）: %q", name)
	}
	dsn, err := dsnFor(name)
	if err != nil {
		return nil, err
	}
	return openGorm(dsn)
}

// projectRoot 通过当前源文件位置向上定位仓库根目录，避免依赖测试运行时的工作目录。
func projectRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("testdb: 无法定位源文件路径")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Setup 为当前用例克隆一个独立数据库（模板已含全部迁移），
// 测试结束后删除该库。需要真实数据库，testing.Short() 模式下会跳过。
func Setup(t *testing.T) *gorm.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("testdb: -short 模式跳过依赖真实 PostgreSQL 的测试")
	}

	name := scratchName()

	// 模板库必须由 scripts/dbinit 预先建好，这里只做检查并给出明确指引
	exists, err := templateExists()
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	if !exists {
		t.Fatalf("testdb: 模板库 %s 不存在，请先运行: go run ./scripts/dbinit", TemplateDBName)
	}

	db := createAndConnect(t, name)

	t.Cleanup(func() {
		defer func() {
			dsn, err := maintDSN()
			if err != nil {
				t.Logf("testdb: 解析连接串失败，无法清理测试库 %s: %v", name, err)
				return
			}
			maint, err := openGorm(dsn)
			if err != nil {
				t.Logf("testdb: 连接维护库失败，无法清理测试库 %s: %v", name, err)
				return
			}
			if err := dropDB(maint, name); err != nil {
				t.Logf("testdb: 清理测试库失败: %v（可手动 DROP DATABASE %s）", err, name)
			}
			if sqlDB, err := maint.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}()
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close() // 先释放本用例连接池，DROP 才不会被自身连接阻塞
		}
	})

	return db
}

// templateExists 检查模板库是否已由 scripts/dbinit 建好。
func templateExists() (bool, error) {
	dsn, err := maintDSN()
	if err != nil {
		return false, err
	}
	maint, err := openGorm(dsn)
	if err != nil {
		return false, fmt.Errorf("连接维护库失败: %w", err)
	}
	defer func() {
		if sqlDB, err := maint.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	return databaseExists(maint, TemplateDBName)
}

// createAndConnect 克隆测试库并建立连接；任一步失败直接终止测试，
// 已克隆的库会被回收，避免残留垃圾库。
func createAndConnect(t *testing.T, name string) *gorm.DB {
	t.Helper()

	dsn, err := maintDSN()
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	maint, err := openGorm(dsn)
	if err != nil {
		t.Fatalf("testdb: 连接维护库失败: %v", err)
	}
	defer func() {
		if sqlDB, err := maint.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	if err := execOn(maint, "CREATE DATABASE "+name+" TEMPLATE "+TemplateDBName); err != nil {
		t.Fatalf("testdb: 从模板克隆数据库 %s 失败: %v", name, err)
	}
	db, err := OpenDB(name)
	if err != nil {
		_ = dropDB(maint, name)
		t.Fatalf("testdb: 连接测试库 %s 失败: %v", name, err)
	}
	return db
}

// scratchName 生成每用例克隆库的名称：test_<pid>_<seq>_<rand>，
// pid 隔离多进程，seq/随机段隔离同进程并发用例。
func scratchName() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("testdb: 生成随机库名失败: %w", err))
	}
	return fmt.Sprintf("%s%d_%d_%s", scratchPrefix, os.Getpid(), scratchSeq.Add(1), hex.EncodeToString(b[:]))
}

// Migrate 执行迁移，失败时终止当前测试（仅 dbinit 建模板时使用）。
func Migrate(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := MigrateDB(db); err != nil {
		t.Fatalf("testdb: %v", err)
	}
}

// MigrateDB 执行 sql/*.sql 中的全部迁移脚本（幂等，脚本内均为 IF NOT EXISTS）。
func MigrateDB(db *gorm.DB) error {
	dir := filepath.Join(projectRoot(), "sql")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("扫描迁移脚本失败 %s: %w", dir, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("未找到迁移脚本，目录: %s", dir)
	}
	sort.Strings(files)

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("读取迁移脚本失败 %s: %w", f, err)
		}
		for _, stmt := range splitStatements(string(raw)) {
			if err := db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("执行迁移失败 %s: %w\nSQL: %s", filepath.Base(f), err, stmt)
			}
		}
	}
	return nil
}

// splitStatements 按分号拆分 SQL，并丢弃仅含注释/空白的片段。
func splitStatements(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ";") {
		if strings.TrimSpace(stripComments(part)) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

// stripComments 去掉行注释，仅用于判断语句是否为空。
func stripComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
