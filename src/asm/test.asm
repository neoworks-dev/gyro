global _start
_start:
        jmp s_0
main:
        push ebp
        mov ebp, esp
        mov eax, [ebp+12]
        mov [ebp-4], eax
        mov eax, [ebp-4]
        pop ebp
        ret
s_0:
        sub esp, 20
        mov dword [esp+0], 1
        mov dword [esp+4], 2
        mov dword [esp+8], 3
        mov dword [esp+12], 4
        mov dword [esp+16], 5
        call main
        add esp, 20
exit:
        mov ebx, eax
        mov eax, 1
        int 0x80