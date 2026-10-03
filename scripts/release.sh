#!/usr/bin/env bash
set -euo pipefail

# 1. 确保在仓库根目录下执行
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

IMAGE="${IMAGE:-teatak/buzz}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
KIND="${1:-patch}"

if [ ! -f VERSION ]; then
  echo "❌ 错误: 根目录下未找到 VERSION 文件"
  exit 1
fi

# 2. 检查基本工具依赖
if ! command -v docker >/dev/null 2>&1; then
  echo "❌ 错误: 未检测到 docker 命令，请先安装 Docker"
  exit 1
fi

# 3. 检查当前分支必须是 main
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
if [ "$CURRENT_BRANCH" != "main" ]; then
  echo "❌ 错误: 发版必须在 main 分支执行 (当前分支: $CURRENT_BRANCH)"
  exit 1
fi

# 4. 检查工作区是否存在除 VERSION 之外未提交的代码改动
DIRTY_FILES=$(git status --porcelain | grep -vE ' (VERSION|VERSION\.tmp)$' || true)
if [ -n "$DIRTY_FILES" ]; then
  echo "❌ 错误: 工作区存在未提交的代码改动，请先提交或 stash 后再执行发版:"
  echo "$DIRTY_FILES"
  exit 1
fi

# 5. 同步远端 tags 与 main 分支，并校验本地是否落后于远程
echo "🔄 正在同步远程仓库信息 (origin/main 及 tags)..."
git fetch origin main --tags >/dev/null 2>&1 || true

LOCAL_COMMIT=$(git rev-parse HEAD)
REMOTE_COMMIT=$(git rev-parse origin/main 2>/dev/null || echo "")
if [ -n "$REMOTE_COMMIT" ]; then
  BASE_COMMIT=$(git merge-base HEAD origin/main)
  if [ "$LOCAL_COMMIT" != "$REMOTE_COMMIT" ] && [ "$BASE_COMMIT" != "$REMOTE_COMMIT" ]; then
    echo "❌ 错误: 本地分支落后于 origin/main，请先执行 git pull 再发版"
    exit 1
  fi
fi

# 6. 执行单元测试预检
echo "🧪 正在执行测试预检 (go test ./...)..."
go test ./...

# 7. 解析版本号并计算目标版本
CURRENT_VERSION=$(tr -d '[:space:]' < VERSION)
if [[ ! "$CURRENT_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "❌ 错误: VERSION 文件内容 ($CURRENT_VERSION) 不是合法的语义化版本 (格式应为 x.y.z)"
  exit 1
fi

LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
CURRENT_TAG="v$CURRENT_VERSION"

case "$KIND" in
  patch|minor|major|current)
    ;;
  *)
    if [[ "$KIND" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      TARGET_VERSION="$KIND"
    else
      echo "❌ 错误: 不支持的发布类型 '$KIND' (可选: patch, minor, major, current 或指定版本号如 0.2.0)"
      exit 1
    fi
    ;;
esac

if [ -z "${TARGET_VERSION:-}" ]; then
  TAG_EXISTS=false
  if git rev-parse -q --verify "refs/tags/$CURRENT_TAG" >/dev/null 2>&1; then
    TAG_EXISTS=true
  fi

  if [ "$TAG_EXISTS" = true ]; then
    # 当前 VERSION 已打过 tag，说明已发布过，按 KIND 递增
    IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"
    case "$KIND" in
      major)
        TARGET_VERSION="$((MAJOR + 1)).0.0"
        ;;
      minor)
        TARGET_VERSION="${MAJOR}.$((MINOR + 1)).0"
        ;;
      patch)
        TARGET_VERSION="${MAJOR}.${MINOR}.$((PATCH + 1))"
        ;;
      current)
        echo "❌ 错误: 标签 $CURRENT_TAG 已经存在，无法重复发布当前版本"
        exit 1
        ;;
    esac
    echo "📦 当前版本 $CURRENT_VERSION 已发布，递增版本: $CURRENT_VERSION -> $TARGET_VERSION ($KIND)"
  else
    # 当前 VERSION 尚未打 tag
    IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"
    case "$KIND" in
      major)
        TARGET_VERSION="$((MAJOR + 1)).0.0"
        echo "📦 升级 major 版本: $CURRENT_VERSION -> $TARGET_VERSION"
        ;;
      minor)
        TARGET_VERSION="${MAJOR}.$((MINOR + 1)).0"
        echo "📦 升级 minor 版本: $CURRENT_VERSION -> $TARGET_VERSION"
        ;;
      current)
        TARGET_VERSION="$CURRENT_VERSION"
        echo "🔄 发布当前未打 tag 版本: $TARGET_VERSION"
        ;;
      patch)
        # 如果当前版本号已经大于最新 tag（例如 latest=v0.1.18, current=0.1.19），
        # 说明当前版本是已就绪待发布的 patch 版本，直接发布；否则正常递增
        if [ -n "$LATEST_TAG" ]; then
          LATEST_VERSION="${LATEST_TAG#v}"
          if [ "$CURRENT_VERSION" != "$LATEST_VERSION" ]; then
            TARGET_VERSION="$CURRENT_VERSION"
            echo "🔄 检测到当前版本 $TARGET_VERSION (最新 Tag 为 $LATEST_TAG) 尚未发布，继续发布该版本..."
          else
            TARGET_VERSION="${MAJOR}.${MINOR}.$((PATCH + 1))"
            echo "📦 递增 patch 版本: $CURRENT_VERSION -> $TARGET_VERSION"
          fi
        else
          TARGET_VERSION="$CURRENT_VERSION"
          echo "🔄 首次发版: $TARGET_VERSION"
        fi
        ;;
    esac
  fi
fi

TARGET_TAG="v$TARGET_VERSION"

# 再次确认目标 tag 是否已存在
if git rev-parse -q --verify "refs/tags/$TARGET_TAG" >/dev/null 2>&1; then
  echo "❌ 错误: 目标标签 $TARGET_TAG 已经存在，无法重复发布！"
  exit 1
fi

# 8. 更新 VERSION 文件并提交
if [ "$TARGET_VERSION" != "$CURRENT_VERSION" ] || ! git diff --quiet VERSION; then
  echo "$TARGET_VERSION" > VERSION
  git add VERSION
  git commit -m "chore: bump version to $TARGET_VERSION"
  echo "✅ 已提交版本文件更新: chore: bump version to $TARGET_VERSION"
fi

# 9. 执行 Docker 构建与发布
echo "🐳 正在构建并推送 Docker 镜像 ($IMAGE:latest, $IMAGE:$TARGET_VERSION)..."
if ! docker buildx build --platform "$PLATFORMS" -t "$IMAGE:latest" -t "$IMAGE:$TARGET_VERSION" --push .; then
  echo "❌ 错误: Docker 镜像构建或推送失败！"
  echo "💡 提示: 本地已生成版本更新提交，若需取消本次发版，请执行 'git reset --hard HEAD~1' 回退；修复 Docker 环境后可直接重新运行发版脚本。"
  exit 1
fi
echo "✅ Docker 镜像推送成功！"

# 10. 创建本地 Git Tag 并显式推送到远程仓库
git tag -a "$TARGET_TAG" -m "Release $TARGET_TAG"
echo "✅ 已创建本地 tag: $TARGET_TAG"

echo "🚀 正在推送分支与标签到远程仓库 ($CURRENT_BRANCH, $TARGET_TAG)..."
git push origin "$CURRENT_BRANCH"
git push origin "$TARGET_TAG"
echo "🎉 发布成功！版本: $TARGET_TAG"
