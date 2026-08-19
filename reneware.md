# Goal

CodeSampleX의 **백엔드, 데이터 수집 구조, Evidence/Sample/Finding 파이프라인은 건드리지 않는다.**

이번 작업의 목적은 **홈페이지 Frontend와 GitHub README의 포지셔닝을 완전히 재정렬하는 것**이다.

현재 CodeSampleX가 자칫 다음처럼 보일 수 있다.

- 코드 샘플 사이트
- LLM knowledge/context 서비스
- MCP 서비스
- Context7와 비슷한 문서/예제 공급 서비스
- Stack Overflow류 지식 공유 서비스

이 인상을 제거해야 한다.

CodeSampleX의 실제 정체성은 다음이다.

> **Developer Compatibility Testing Network**
>
> 실제 개발 환경에서 직접 실행하고 테스트한 결과를 축적하여  
> 무엇이 어디서 실제로 동작했는지를 보여주는 공개 검증망.

핵심 철학:

> **We tested it. This is what happened.**

또는 더 짧게:

> **Tested. Not guessed.**

CodeSampleX는 문서를 설명하는 것이 아니다.  
누군가의 경험담을 모으는 것도 아니다.

**실제로 실행해보고, 측정하고, 재검증한다.**

---

# Absolute Rules

## 1. Backend 변경 금지

이번 작업에서는 특별한 이유가 없는 한 다음을 수정하지 않는다.

- API 서버
- DB schema
- worker
- verification pipeline
- evidence collection
- sample generation pipeline
- finding pipeline
- existing compatibility data

현재 백엔드가 제공하는 데이터만으로 Frontend를 최대한 재구성한다.

필요한 데이터가 정말 API에 없어서 UI 구현이 불가능한 경우에만 먼저 보고하고 최소 변경을 제안한다.

임의로 backend를 확장하지 않는다.

---

## 2. MCP를 제품 정체성으로 표현하지 않는다

MCP는 CodeSampleX의 본체가 아니다.

관계는 다음과 같다.

```text
CodeSampleX
├─ CLI        ← primary user tool
├─ API        ← automation / integration
├─ Web        ← compatibility map / reports / manual
└─ MCP        ← agent adapter
```

MCP는 LLM/Agent가 CodeSampleX를 사용하기 위한 **adapter**일 뿐이다.

홈페이지와 README에서 MCP가 제품 자체처럼 보이지 않게 한다.

---

# Core Positioning

CodeSampleX의 핵심 질문은:

> **Does it run there?**

사람이 궁금한 것은:

- 이 OS에서 되는가?
- 이 runtime 버전에서 되는가?
- 이 package version에서 되는가?
- 이 JDK에서 되는가?
- 이 Gradle/Maven 조합에서 되는가?
- Windows에서는 되고 Linux에서는 안 되는가?
- 실제로 실행해본 기록이 있는가?

CodeSampleX가 제공하는 답은:

> **We tested it.**

---

# Primary Visual Identity

홈페이지의 주인공은 검색창이 아니라 **Compatibility Matrix**여야 한다.

첫 방문자가 3~5초 안에 다음을 이해해야 한다.

> "아, 여기는 여러 실제 환경에서 직접 테스트해서 PASS/FAIL 지도를 만드는 곳이구나."

예시:

```text
Target: Node.js

             Node 20   Node 22   Node 24
Windows 11     PASS      PASS      FAIL
Ubuntu 24      PASS      PASS      PASS
macOS ARM      PASS       ?         ?
```

또는 Java:

```text
Target: Example Library

             JDK 17   JDK 21   JDK 25
Maven 3.9      PASS     PASS     FAIL
Gradle 8       PASS     PASS      ?
```

---

# Matrix Requirements

Matrix는 단순 mock UI가 아니라 가능하면 현재 실제 데이터를 사용한다.

사용자가 축을 변경할 수 있게 한다.

예:

```text
X Axis
- Runtime Version
- Package Version
- Engine Version
- JDK Version
- Tool Version

Y Axis
- OS
- Architecture
- Build Tool
- Runtime
- Environment
```

모든 대상에 모든 축을 강제하지 않는다.

대상별로 의미 있는 조합을 선택한다.

예:

```text
Node/npm:
OS × Node Version

Java:
JDK × Maven/Gradle Version

Unreal:
OS × Engine Version

CLI:
OS × CLI Version

Python:
Python Version × OS
```

---

# Matrix Cell States

최소 다음 의미를 명확하게 표현한다.

```text
PASS
FAIL
UNKNOWN
STALE
CROSS-CHECKED
```

기존 CodeSampleX의 실제 evidence grade가 더 세분화되어 있다면 그 구조를 존중한다.

색깔 하나만으로 의미를 전달하지 않는다. 텍스트/아이콘/tooltip도 같이 사용한다.

셀 클릭 시 가능한 경우 다음 정보로 연결한다.

```text
Environment
Version
Last tested
Evidence count
Verification grade
Finding
Verified Sample
Cross-check history
```

---

# Homepage Information Architecture

홈페이지의 흐름을 다음 철학으로 재구성한다.

## 1. Hero

제품 설명보다 **검사 결과를 먼저 보여준다.**

추천 방향:

```text
Does it run there?

Tested across real environments.
Not guessed from documentation.
```

또는:

```text
Tested. Not guessed.

See where developer tools actually work.
```

과장된 AI 마케팅 문구는 피한다.

---

## 2. Compatibility Matrix

Hero 바로 아래 또는 Hero 자체에 Matrix를 배치한다.

이게 CodeSampleX의 대표 UI가 되어야 한다.

---

## 3. Findings

다음 질문에 답한다.

> **Where does it break?**

Finding은 단순 게시글처럼 보이면 안 된다.

실제로 측정하다 발견한:

- documentation mismatch
- environment-specific failure
- version boundary
- unexpected runtime behavior
- compatibility exception

등으로 표현한다.

Finding 숫자를 메인 KPI로 크게 내세울 필요는 없다.

대표 Finding 몇 개를 실제 사례로 보여주는 것이 더 중요하다.

---

## 4. Verified Samples

다음 질문에 답한다.

> **Then how do I use it?**

Sample은 일반 코드 snippets가 아니다.

실제로 실행되고 검증된 답안이라는 점을 강하게 표현한다.

---

## 5. Evidence

다음 질문에 답한다.

> **Why should I trust this result?**

Evidence는 사용자에게 직접적인 상품이라기보다 **신뢰 계층**이다.

표현 예:

```text
Observed
Verified
Cross-checked
Multi-environment verified
```

현재 존재하는 실제 grade 체계를 기반으로 명확한 Evidence Ladder/Legend를 제공한다.

---

## 6. CLI Installation

CLI가 본 제품이라는 인상을 준다.

현재 실제 CLI 명령을 반드시 먼저 확인하고 존재하지 않는 명령을 임의로 만들지 않는다.

CLI로 할 수 있는 일 중심으로 설명한다.

그 다음에:

```text
Agent integration
MCP
API
Prompt installation
```

등을 secondary integration으로 보여준다.

---

# Homepage Metrics

현재처럼 다음 3개는 주요 숫자로 유지한다.

```text
Packages
Evidence
Verified Samples
```

각 숫자가 무엇을 의미하는지도 자연스럽게 전달한다.

- Packages = coverage
- Evidence = measured observations
- Verified Samples = usable answers

Finding count는 메인 숫자에서 제외해도 된다.

---

# Desired Visual Tone

다음 느낌을 지향한다.

- compatibility lab
- benchmark site
- diagnostics tool
- antivirus scanner
- hardware compatibility database
- CI test report
- engineering measurement dashboard

다음 느낌은 피한다.

- AI SaaS landing page
- generic MCP directory
- documentation portal
- Q&A community
- prompt marketplace
- ChatGPT wrapper

전체적으로 **측정기 / 검사기 / 개발환경 실험실** 느낌이 나야 한다.

---

# Antivirus Analogy

내부 디자인 사고에는 다음 비유를 사용할 수 있다.

```text
Finding  = detected issue / signature
Sample   = verified remedy / known-good path
Evidence = scan/test record
Worker   = scanning engine
Matrix   = compatibility/threat map
CLI      = local scanner
```

하지만 UI에서 억지로 바이러스/백신 용어를 사용할 필요는 없다.

제품 분위기와 정보구조를 잡는 참고 개념이다.

---

# GitHub README Rewrite

README도 홈페이지와 같은 포지셔닝으로 완전히 수정한다.

첫 문단에서 CodeSampleX가 무엇인지 바로 이해되어야 한다.

예시 방향:

> CodeSampleX tests developer tools across real environments and records what actually works.

또는:

> CodeSampleX is an open compatibility testing network for developer tools, runtimes, packages, SDKs, CLIs, and engines.

그 직후 실제 Matrix 예시를 보여준다.

---

# README Ordering

추천 순서:

```text
1. What CodeSampleX is
2. Compatibility Matrix example
3. Why testing matters
4. CLI installation
5. Check/use workflow
6. Samples
7. Findings
8. Evidence / grading
9. API
10. MCP adapter
11. Contribution / privacy
12. Architecture
```

MCP를 상단 핵심 기능으로 두지 않는다.

---

# Important Competitive Separation

README/Homepage 어디에서도 경쟁자를 공격적으로 비교할 필요는 없다.

하지만 제품 차이는 자연스럽게 드러나야 한다.

CodeSampleX는:

```text
Documentation        → what should work
Code search           → how somebody used it
Community knowledge   → what somebody says worked
CodeSampleX           → what was actually tested
```

이 차이를 UI와 문구로 보여준다.

경쟁사 이름을 굳이 직접 언급하지 않아도 된다.

---

# Scope Expansion

CodeSampleX는 이제 package만 다루는 서비스가 아니다.

현재/미래 대상에는 다음이 포함될 수 있다.

```text
Libraries
Packages
SDKs
CLIs
Build tools
Runtimes
Game engines
Operating systems
System tools
Developer toolchains
```

따라서 UI 카피에서 지나치게 `package`에만 한정하지 않는다.

좋은 상위 개념은:

```text
developer tools
developer environments
software environments
toolchains
```

등이다.

---

# Privacy / Transparency

CodeSampleX의 강점인 공개성과 투명성은 유지한다.

명확하게 보여줄 내용:

- source is open
- evidence is inspectable
- verification is reproducible where possible
- no private source code is uploaded automatically
- no secrets
- no local project paths
- no raw private logs automatically shared

기존 실제 privacy contract를 확인하여 정확하게 표현한다.

---

# Do Not Do

- Backend를 필요 없이 리팩터링하지 않는다.
- 새로운 product feature를 발명하지 않는다.
- 데이터 모델을 갈아엎지 않는다.
- MCP 중심 landing page로 만들지 않는다.
- Chat/AI animation 같은 장식을 핵심으로 넣지 않는다.
- 검색창을 Hero의 주인공으로 만들지 않는다.
- Evidence 숫자만 크게 보여주고 의미 설명을 생략하지 않는다.
- 존재하지 않는 CLI/API 기능을 README에 쓰지 않는다.
- 실제 측정값과 mock 결과를 섞어 사용자를 오해시키지 않는다.

---

# Acceptance Criteria

작업 완료 후 처음 방문한 개발자가 5초 안에 다음을 이해해야 한다.

> **CodeSampleX는 실제 개발 환경에서 직접 실행 테스트한 호환성 결과를 보여주는 곳이다.**

그리고 30초 안에는:

> OS/Runtime/Version별 Matrix가 있고  
> 깨지는 곳에는 Finding이 있고  
> 사용할 수 있는 Verified Sample이 있고  
> 각 결과에는 Evidence가 붙어 있으며  
> CLI로 자신의 개발 흐름에서도 사용할 수 있다.

를 이해해야 한다.

GitHub README를 처음 본 사람 역시:

> "또 하나의 Context/MCP/Documentation 도구"

라고 생각하면 실패다.

대신:

> **"아, 얘네는 실제로 여러 환경에서 돌려보고 호환성 지도를 만드는구나."**

라는 반응이 나와야 성공이다.

---

# Final Principle

CodeSampleX의 중심 문장은 이것이다.

> **We tested it. This is what happened.**

그리고 모든 디자인, 문구, README 구조는 이 말을 증명해야 한다.
