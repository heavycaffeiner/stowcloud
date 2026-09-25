// Package bad is a fixture. Every line below carries Korean text and koscan
// has to report all of them.
package bad

// 이 주석은 한국어다.
const message = "사용자를 찾을 수 없습니다"

func log() string {
	return message // 로그 문장
}
