# Stowcloud

[![verify](https://github.com/heavycaffeiner/stowcloud/actions/workflows/verify.yml/badge.svg)](https://github.com/heavycaffeiner/stowcloud/actions/workflows/verify.yml)
[![docker](https://github.com/heavycaffeiner/stowcloud/actions/workflows/docker.yml/badge.svg)](https://github.com/heavycaffeiner/stowcloud/actions/workflows/docker.yml)

[English](README.md) | **한국어**

## 설치

Stowcloud는 Docker 20.10 이상이 설치된 리눅스에서 실행됩니다. 호스트의 리눅스 커널은 5.6 이상이어야 합니다.

### 1. Compose 파일을 받습니다

```sh
wget https://raw.githubusercontent.com/heavycaffeiner/stowcloud/master/compose.yml
```

### 2. 파일을 저장할 위치를 정합니다

기본 Compose 파일은 `/shares/files`에 Docker 볼륨을 연결합니다. 기본 설정을 그대로 사용한다면 다음 단계로 넘어갑니다.

호스트에 있는 기존 폴더를 사용하려면 `sc`와 `sc-smb`의 `sc-files` 마운트를 모두 다음과 같이 바꿉니다.

```yaml
volumes:
  - /srv/my-files:/shares/files:z
```

컨테이너를 시작하기 전에 호스트 디렉터리를 만듭니다. `PUID`와 `PGID`는 그 디렉터리의 소유자에 맞춥니다.

```sh
stat -c '%u:%g' /srv/my-files
```

```yaml
environment:
  PUID: 1000
  PGID: 1000
```

### 3. Stowcloud를 실행하고 관리자 계정을 만듭니다

```sh
docker compose up -d
```

이제 `https://<서버 주소>:8443`에서 Stowcloud에 접속할 수 있습니다. 처음에는 서버가 자체 서명 인증서를 사용하므로 브라우저에 인증서 경고가 표시됩니다.

한 번만 사용할 수 있는 설정 토큰을 확인합니다.

```sh
docker compose exec sc cat /var/lib/stowcloud/setup-token
```

`https://<서버 주소>:8443/setup`을 열고 토큰을 입력한 뒤 관리자 아이디와 비밀번호를 정합니다.

![설정 토큰, 관리자 아이디, 비밀번호를 입력받는 최초 실행 화면](docs/screenshots/setup.png)

로그인한 뒤 관리자 화면에서 마운트한 폴더를 추가합니다. 인터넷에서 접속하게 하려면 신뢰할 수 있는 인증서를 사용하는 리버스 프록시를 Stowcloud 앞에 둡니다.

## 가지고 있는 폴더를 어디서나 사용하세요

Stowcloud는 리눅스 서버에 이미 있는 폴더에 웹 파일 관리, 공유, WebDAV, SMB, 동기화 클라이언트 연결 기능을 더합니다. 파일을 전용 저장 구조로 가져오지 않습니다. 기존 프로그램은 같은 경로를 계속 읽고 쓸 수 있습니다.

사진 라이브러리, 프로젝트 자료, 가족 공유 폴더, 미디어 모음을 Stowcloud에 연결하세요. 휴대폰에서 둘러보고, 네트워크 드라이브로 연결하고, 한 사람에게 특정 하위 폴더만 열어 주거나, 사본을 만들지 않고 공개 링크를 보낼 수 있습니다.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/browse-dark.png">
  <img alt="Stowcloud 파일 브라우저가 home 폴더를 표로 보여 주는 화면. 왼쪽 내비게이션, 경로 표시줄, 정렬 컨트롤, 파일 크기와 수정 날짜가 보인다" src="docs/screenshots/browse-light.png">
</picture>

## 주요 기능

- **파일 관리:** 브라우저에서 파일을 업로드하고 다운로드하며 이동, 복사, 이름 변경, 삭제할 수 있습니다.
- **이어받는 업로드:** 연결이 끊기거나 탭이 닫혀도 대용량 전송을 이어서 진행합니다.
- **폴더 통합 검색:** 계정이 접근할 수 있는 모든 폴더를 검색하고 항목 종류나 파일 종류로 결과를 좁힙니다.
- **공개 링크:** 비밀번호, 만료일, 다운로드 횟수 제한, 업로드 전용 접근을 선택해 폴더를 공유합니다.
- **폴더별 권한:** 계정마다 공유 전체 또는 특정 하위 폴더를 열어 주고 읽기와 쓰기 권한을 따로 정합니다.
- **네트워크 드라이브:** Windows, macOS, Linux에서 WebDAV로 연결합니다. 필요할 때 관리자 화면에서 SMB를 켤 수 있습니다.
- **Nextcloud 앱 호환:** Stowcloud는 Nextcloud 데스크톱 및 모바일 앱과 호환됩니다.[^nextcloud]
- **계정 보안:** 자체 계정, OIDC 로그인, 인증 앱 코드, 앱 비밀번호, 복구 코드를 지원합니다.
- **브라우저 편집기:** 작은 텍스트 파일과 소스 파일을 문법 강조와 함께 편집합니다.
- **폴더별 휴지통:** 복구가 필요한 폴더에만 휴지통을 켭니다.
- **반응형 화면:** 데스크톱과 모바일 브라우저에서 같은 파일 모음을 관리합니다.

## 저장 구조를 바꾸지 않고 탐색합니다

공유는 디스크에 이미 있는 디렉터리를 그대로 따라갑니다. 폴더 트리, 파일 이름, 계층 구조는 서버의 다른 프로그램에서도 계속 사용할 수 있습니다.

![폴더 트리를 펼친 파일 브라우저. home 루트와 Documents 아래의 중첩된 폴더가 보인다](docs/screenshots/tree.png)

## 접근 가능한 모든 폴더를 한 번에 검색합니다

한 번의 검색으로 현재 계정에 허용된 모든 폴더를 확인합니다. 파일, 폴더, 일반적인 파일 종류, 직접 입력한 확장자로 결과를 좁힐 수 있습니다. 데스크톱에서는 `Ctrl+K` 또는 `Cmd+K`로 어느 화면에서든 검색을 엽니다.

![파일 브라우저 위에 열린 검색 시트. 2026 검색어, 검색어 옆의 대상 버튼과 파일 종류 메뉴, 두 공유에서 나온 결과가 각각 폴더와 크기, 날짜와 함께 보인다](docs/screenshots/search.png)

## 계정이 없는 사람에게도 공유합니다

폴더의 공개 링크를 만들면서 만료일, 비밀번호, 다운로드 횟수 제한, 업로드 전용 모드를 선택할 수 있습니다. 생성된 URL은 표시될 때 복사합니다.

![링크를 막 만든 직후의 공유 대화 상자. 한 번만 보이는 URL, 복사 버튼, 만료일](docs/screenshots/share-link.png)

받는 사람은 공유한 폴더만 보여 주는 화면을 사용합니다. Stowcloud 계정은 필요하지 않습니다.

![받는 사람이 보는 공개 공유 페이지. 제목, 공유된 폴더의 파일 목록, 다운로드 버튼](docs/screenshots/share-public.png)

## 사람마다 필요한 폴더만 엽니다

새 계정에는 처음부터 폴더 접근 권한이 없습니다. 관리자는 공유 전체 또는 선택한 하위 폴더를 허용하고 사용할 수 있는 작업을 정합니다. 각 계정은 사이드바 순서도 따로 정할 수 있습니다.

![계정의 폴더 권한 대화 상자. 루트 범위의 읽기와 다운로드 권한, 상세 보기와 수정 및 제거 컨트롤이 보인다](docs/screenshots/folder-grants.png)

## 브라우저에서 텍스트를 편집합니다

작은 텍스트 파일과 소스 파일을 내려받지 않고 바로 엽니다. 편집기는 문법 강조, 줄 번호, 저장되지 않은 상태 표시를 제공하며 다른 곳에서 파일이 바뀌면 충돌을 알려 줍니다.

![언어 배지, 문법 강조, 저장되지 않은 상태, 저장 컨트롤, 줄 번호가 표시된 내장 편집기에서 열린 TypeScript 파일](docs/screenshots/editor.png)

## 삭제한 파일을 복구합니다

공유 폴더마다 휴지통을 따로 켤 수 있습니다. 실수로 지운 항목은 복원하고, 더 필요하지 않은 항목은 영구 삭제합니다.

![삭제된 항목의 크기와 삭제 시각, 복원과 영구 삭제 버튼이 있는 휴지통 화면](docs/screenshots/trash.png)

Stowcloud는 오픈 소스 소프트웨어입니다.[^license]

[^license]: Stowcloud는 GNU Affero General Public License v3.0 이상으로 배포됩니다. 자세한 내용은 [`LICENSE`](LICENSE)를 확인하세요.

[^nextcloud]: Nextcloud는 Nextcloud GmbH의 등록 상표입니다. Stowcloud는 Nextcloud GmbH와 제휴, 보증, 후원 관계가 없습니다. 해당 명칭은 앱 호환성을 설명하기 위해서만 사용합니다.
