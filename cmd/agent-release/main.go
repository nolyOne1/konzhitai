package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yunling.local/platform/internal/agentrelease"
	"yunling.local/platform/internal/artifact"
	storepostgres "yunling.local/platform/internal/store/postgres"
)

type importConfiguration struct {
	ManifestPath string
	Directory    string
	ReleaseNotes string
	Recommend    bool
}

type commandArtifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	FileName string `json:"file_name"`
	ByteSize int64  `json:"byte_size"`
	SHA256   string `json:"sha256"`
}

type commandManifest struct {
	Version   string            `json:"version"`
	Artifacts []commandArtifact `json:"artifacts"`
}

func main() {
	configuration, err := parseArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	dsn := strings.TrimSpace(os.Getenv("YUNLING_DATABASE_URL"))
	if dsn == "" {
		log.Fatal("未设置 YUNLING_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := storepostgres.Open(ctx, dsn)
	if err != nil {
		log.Fatalf("连接数据库失败：%v", err)
	}
	defer db.Close()
	secure, _ := strconv.ParseBool(os.Getenv("YUNLING_S3_SECURE"))
	objects, err := artifact.NewMinIOStore(artifact.MinIOConfig{
		Endpoint: os.Getenv("YUNLING_S3_ENDPOINT"), AccessKey: os.Getenv("YUNLING_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("YUNLING_S3_SECRET_KEY"), Bucket: os.Getenv("YUNLING_S3_BUCKET"), Secure: secure,
	})
	if err != nil {
		log.Fatalf("连接对象存储失败：%v", err)
	}
	service := agentrelease.NewService(agentrelease.NewPostgresRepository(db), objects, time.Now)
	release, err := executeImport(ctx, configuration, service)
	if err != nil {
		log.Fatalf("导入代理版本失败：%v", err)
	}
	fmt.Printf("代理版本 %s 导入成功", release.Version)
	for _, item := range release.Artifacts {
		fmt.Printf("，%s %s", item.Arch, item.SHA256[:12])
	}
	if release.Recommended {
		fmt.Print("，已设为推荐版本")
	}
	fmt.Println()
}

type releaseImporter interface {
	Import(context.Context, agentrelease.ImportInput) (agentrelease.Release, error)
}

func executeImport(ctx context.Context, configuration importConfiguration, importer releaseImporter) (agentrelease.Release, error) {
	input, err := loadImportInput(configuration)
	if err != nil {
		return agentrelease.Release{}, err
	}
	return importer.Import(ctx, input)
}

func parseArgs(args []string) (importConfiguration, error) {
	var configuration importConfiguration
	if len(args) == 0 || args[0] != "import" {
		return configuration, errors.New("用法：yunling-agent-release import --manifest 路径 --directory 目录 --notes 说明 [--recommend]")
	}
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&configuration.ManifestPath, "manifest", "", "清单路径")
	flags.StringVar(&configuration.Directory, "directory", "", "安装包目录")
	flags.StringVar(&configuration.ReleaseNotes, "notes", "", "发布说明")
	flags.BoolVar(&configuration.Recommend, "recommend", false, "设为推荐版本")
	if err := flags.Parse(args[1:]); err != nil {
		return configuration, fmt.Errorf("解析导入参数：%w", err)
	}
	if flags.NArg() != 0 || strings.TrimSpace(configuration.ManifestPath) == "" || strings.TrimSpace(configuration.Directory) == "" {
		return configuration, errors.New("必须提供清单路径和安装包目录")
	}
	return configuration, nil
}

func loadImportInput(configuration importConfiguration) (agentrelease.ImportInput, error) {
	manifestInfo, err := os.Lstat(configuration.ManifestPath)
	if err != nil {
		return agentrelease.ImportInput{}, fmt.Errorf("读取代理清单：%w", err)
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 1<<20 {
		return agentrelease.ImportInput{}, errors.New("代理清单必须是小于 1 MiB 的普通文件")
	}
	manifestBody, err := os.ReadFile(configuration.ManifestPath)
	if err != nil {
		return agentrelease.ImportInput{}, fmt.Errorf("读取代理清单：%w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBody))
	decoder.DisallowUnknownFields()
	var manifest commandManifest
	if err := decoder.Decode(&manifest); err != nil {
		return agentrelease.ImportInput{}, fmt.Errorf("解析代理清单：%w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return agentrelease.ImportInput{}, errors.New("代理清单包含尾随内容")
	}
	manifestDigest := sha256.Sum256(manifestBody)
	input := agentrelease.ImportInput{Version: manifest.Version, ReleaseNotes: configuration.ReleaseNotes, Recommend: configuration.Recommend, ManifestSHA256: hex.EncodeToString(manifestDigest[:])}
	for _, item := range manifest.Artifacts {
		if filepath.Base(item.FileName) != item.FileName {
			return agentrelease.ImportInput{}, errors.New("代理安装包文件名无效")
		}
		path := filepath.Join(configuration.Directory, item.FileName)
		info, err := os.Lstat(path)
		if err != nil {
			return agentrelease.ImportInput{}, fmt.Errorf("读取代理安装包 %s：%w", item.FileName, err)
		}
		if !info.Mode().IsRegular() || info.Size() != item.ByteSize {
			return agentrelease.ImportInput{}, fmt.Errorf("代理安装包大小不符：%s", item.FileName)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return agentrelease.ImportInput{}, fmt.Errorf("打开代理安装包 %s：%w", item.FileName, err)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != item.SHA256 {
			return agentrelease.ImportInput{}, fmt.Errorf("代理安装包摘要不符：%s", item.FileName)
		}
		if err := agentrelease.ValidateArchive(body, manifest.Version); err != nil {
			return agentrelease.ImportInput{}, fmt.Errorf("校验代理安装包 %s：%w", item.FileName, err)
		}
		input.Artifacts = append(input.Artifacts, agentrelease.ImportArtifact{OS: item.OS, Arch: item.Arch, FileName: item.FileName, ByteSize: item.ByteSize, SHA256: item.SHA256, Body: bytes.NewReader(body)})
	}
	return input, nil
}
