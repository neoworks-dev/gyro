-- lazy.nvim spec for Gyro editor support: the Gyro language and the METALS
-- IR it compiles to.
--
-- The tree-sitter parsers are compiled straight into ~/.config/nvim/parser/
-- and the queries are symlinked into ~/.config/nvim/queries/{gyro,metals};
-- run editor/nvim/install.sh in the gyro repo to (re)do both. The language
-- server is the compiler itself: `gyro lsp`.

local REPO_ROOT = "/home/moritz/Documents/neoworks/gyro-lang/gyro"

return {
  name = "gyro",
  dir = REPO_ROOT .. "/editor/nvim",
  lazy = false,
  priority = 100,
  config = function()
    -- 1. Recognise the file extensions.
    vim.filetype.add({
      extension = {
        gyro = "gyro",
        metals = "metals",
      },
    })

    -- 2. Map each filetype to its tree-sitter parser of the same name.
    vim.treesitter.language.register("gyro", "gyro")
    vim.treesitter.language.register("metals", "metals")

    -- 3. The language server (Neovim 0.11+). One server speaks both
    -- languages; it tells them apart by file extension.
    vim.lsp.config.gyro = {
      cmd = { REPO_ROOT .. "/bin/gyro", "lsp" },
      filetypes = { "gyro", "metals" },
      root_markers = { ".git", "gyro.toml" },
    }
    vim.lsp.enable("gyro")

    -- 4. Inlay hints are off by default in Neovim; turn them on per buffer
    -- once the server attaches, and put a toggle on <leader>ih.
    vim.api.nvim_create_autocmd("LspAttach", {
      callback = function(event)
        local client = vim.lsp.get_client_by_id(event.data.client_id)
        if not client or client.name ~= "gyro" then
          return
        end
        vim.lsp.inlay_hint.enable(true, { bufnr = event.buf })
        vim.keymap.set("n", "<leader>ih", function()
          local filter = { bufnr = event.buf }
          vim.lsp.inlay_hint.enable(not vim.lsp.inlay_hint.is_enabled(filter), filter)
        end, { buffer = event.buf, desc = "Toggle inlay hints" })
      end,
    })

    -- 5. Tree-sitter highlighting and folding for both filetypes.
    vim.api.nvim_create_autocmd("FileType", {
      pattern = { "gyro", "metals" },
      callback = function()
        pcall(vim.treesitter.start)
        vim.wo.foldmethod = "expr"
        vim.wo.foldexpr = "v:lua.vim.treesitter.foldexpr()"
        vim.wo.foldenable = false
      end,
    })
  end,
}
