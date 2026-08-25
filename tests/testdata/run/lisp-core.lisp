; The Lisp front end on its own, with no Gyro in the program at all.
;
; Everything here is ordinary Scheme surface: the only thing this dialect
; adds is the type declaration, `(: name type)`, which is a list like any
; other and is how a typed Lisp has always spelled it. Declaring the
; types is what lets a call from another language be a direct call rather
; than something carrying a tag.

(export main)

(: gcd (-> i64 i64 i64))
(define (gcd a b)
  (if (= b 0) a (gcd b (mod a b))))

; Definitions may call each other in any order: every signature is
; collected before any body is compiled.
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
    ((even-p n) "positive even")
    (else "positive odd")))

; let, let* and set! over a local the compiler keeps in a register.
(: sum-to (-> i64 i64))
(define (sum-to n)
  (let ((total 0))
    (begin
      (set! total (loop-add n 1 total))
      total)))

(: loop-add (-> i64 i64 i64 i64))
(define (loop-add n i total)
  (if (> i n) total (loop-add n (+ i 1) (+ total i))))

(: area (-> f64 f64))
(define (area r)
  (* 3.14159 (* r r)))

; A lambda in a checking position needs no annotation; apply-i64 says
; what it takes.
(: apply-i64 (-> (-> i64 i64) i64 i64))
(define (apply-i64 f x) (f x))

(: scale-by (-> i64 i64 i64))
(define (scale-by factor x)
  (apply-i64 (lambda (v) (* v factor)) x))

; An internal define is how a Scheme names a local function. It lifts to
; the same closure the lambda above does.
(: offset-by (-> i64 i64 i64))
(define (offset-by base x)
  (: shift (-> i64 i64))
  (define (shift v) (+ v base))
  (shift x))

; Closures nest: the inner one captures through the outer one.
(: curried-add (-> i64 (-> i64 (-> i64 i64))))
(define (curried-add a)
  (lambda (b) (lambda (c) (+ a (+ b c)))))

(: main (-> void))
(define (main)
  (displayln (gcd 1071 462))
  (displayln (classify -4))
  (displayln (classify 0))
  (displayln (classify 12))
  (displayln (classify 7))
  (displayln (sum-to 100))
  (displayln (area 2.0))
  (displayln (string-append "a" "b" "c"))

  ; short-circuiting connectives and chained comparison
  (displayln (and (< 1 2) (< 2 3)))
  (displayln (or (> 1 2) (= 5 5)))
  (displayln (< 1 2 3 4))
  (displayln (< 1 2 9 4))
  (displayln (not (= 1 1)))

  (displayln (scale-by 3 14))
  (displayln (offset-by 100 23))
  (displayln (((curried-add 1) 2) 3))

  ; display does not end the line; newline does
  (display "done")
  (newline))
