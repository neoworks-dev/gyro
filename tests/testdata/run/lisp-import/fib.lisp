; A module in another language, compiled to METALS by internal/lisp and
; linked with the Gyro program that imports it.
;
; The types are declared, so the call from Gyro is an ordinary typed
; call: nothing is tagged, boxed or inspected at run time.

(export fib gcd classify scale_by offset_by)

(: fib (-> i64 i64))
(define (fib n)
  (if (< n 2)
      n
      (+ (fib (- n 1)) (fib (- n 2)))))

(: gcd (-> i64 i64 i64))
(define (gcd a b)
  (if (= b 0) a (gcd b (mod a b))))

; Mutual recursion across declarations the compiler saw before either
; body, and a string result crossing the boundary.
(: even-p (-> i64 bool))
(define (even-p n)
  (if (= n 0) #t (odd-p (- n 1))))

(: odd-p (-> i64 bool))
(define (odd-p n)
  (if (= n 0) #f (even-p (- n 1))))

(: classify (-> i64 str))
(define (classify n)
  (cond
    ((< n 0) "negative")
    ((= n 0) "zero")
    ((even-p n) "even")
    (else "odd")))

; A closure built inside the Lisp module and applied there: the captured
; environment is heap storage, which is what METALS requires of one.
; The lambda needs no annotation because apply-i64 declares what it takes.
;
; Only exported names have to be spellable on both sides of the boundary,
; which is why even-p, odd-p and apply-i64 keep their hyphens and the
; exported names do not.
(: apply-i64 (-> (-> i64 i64) i64 i64))
(define (apply-i64 f x)
  (f x))

(: scale_by (-> i64 i64 i64))
(define (scale_by factor x)
  (apply-i64 (lambda (v) (* v factor)) x))

; The same closure written as an internal define, which is how a Scheme
; names one. Both spellings lift to a top-level function with a heap
; environment; nothing here is syntax an ordinary Scheme would reject.
(: offset_by (-> i64 i64 i64))
(define (offset_by base x)
  (: shift (-> i64 i64))
  (define (shift v) (+ v base))
  (shift x))
