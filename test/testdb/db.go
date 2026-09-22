// Package testdb 提供依赖真实 PostgreSQL 的集成测试基础设施：
// 连接 test 库、执行迁移（sql/*.sql）、清空表。
//
// 默认连接串可用环境变量 TEST_PG_URL 覆盖：
//
//	TEST_PG_URL=postgres://user:pass@host:5432/test?sslmode=disable go test ./internal/media/v1/...
//
// 使用方式：
//
//	func TestXxx(t *testing.T) {
//	    db := testdb.Setup(t) // 自动迁移 + 清空表 + 注册清理
//	    repo := v1.NewMediaRepo(db)
//	    ...
//	}
package testdb

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DefaultDSN 测试库默认连接串（指向远程 test 库，不走生产库）
const DefaultDSN = "postgres://postgres:postgres123@106.53.172.186:5432/test?sslmode=disable"

// tables 需要清空的表，顺序无关（TRUNCATE 一次性处理）
var tables = []string{
	"media_assets",
	"media_files",
	"out_box_entries",
}

// dbNameRe 限制数据库名，避免拼接到 CREATE DATABASE 时出现注入。
var dbNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DSN 返回测试库连接串，优先使用 TEST_PG_URL。
func DSN() string {
	if v := strings.TrimSpace(os.Getenv("TEST_PG_URL")); v != "" {
		return v
	}
	return DefaultDSN
}

// EnsureDatabase 连接维护库(postgres)，在目标库不存在时创建它。
// 可用 `go run ./test/testdb/dbinit` 直接调用。
func EnsureDatabase() error {
	u, err := url.Parse(DSN())
	if err != nil {
		return fmt.Errorf("解析连接串失败: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return fmt.Errorf("连接串缺少数据库名: %s", DSN())
	}
	if !dbNameRe.MatchString(name) {
		return fmt.Errorf("数据库名不合法（仅允许字母/数字/下划线）: %q", name)
	}

	maint := *u
	maint.Path = "/postgres"
	db, err := gorm.Open(postgres.Open(maint.String()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("连接维护库失败 (%s): %w", maint.String(), err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	var exists bool
	if err := db.Raw("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = ?)", name).Scan(&exists).Error; err != nil {
		return fmt.Errorf("查询数据库是否存在失败: %w", err)
	}
	if exists {
		return nil
	}
	if err := db.Exec("CREATE DATABASE " + name).Error; err != nil {
		return fmt.Errorf("创建数据库 %s 失败: %w", name, err)
	}
	return nil
}

// projectRoot 通过当前源文件位置向上定位仓库根目录，避免依赖测试运行时的工作目录。
func projectRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("testdb: 无法定位源文件路径")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Setup 连接 test 库，执行迁移并清空所有表；测试结束后再次清空。
// 需要真实数据库，因此 testing.Short() 模式下会跳过。
func Setup(t *testing.T) *gorm.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("testdb: -short 模式跳过依赖真实 PostgreSQL 的测试")
	}

	db, err := Open()
	if err != nil {
		t.Fatalf("testdb: 连接测试库失败 (%s): %v", DSN(), err)
	}

	Migrate(t, db)
	Truncate(t, db)

	t.Cleanup(func() {
		Truncate(t, db)
	})

	return db
}

// Open 仅建立连接（不迁移、不清表），便于工具类调用方自行控制生命周期。
func Open() (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(DSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
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

// Migrate 执行迁移，失败时终止当前测试。
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

// Truncate 清空所有业务表，失败时终止当前测试。
func Truncate(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := TruncateDB(db); err != nil {
		t.Fatalf("testdb: %v", err)
	}
}

// TruncateDB 清空所有业务表并重置自增序列。
func TruncateDB(db *gorm.DB) error {
	stmt := "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("清空表失败: %w", err)
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
