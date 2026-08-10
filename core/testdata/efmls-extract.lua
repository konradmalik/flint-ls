-- Dumps every linter and formatter config from efmls-configs-nvim as json on
-- stdout. The compatibility test in core/efmls_configs_test.go runs this over the
-- corpus it downloaded; there is no reason to run it by hand.
--
-- The configs are lua that decides what to return from the machine it loads on:
-- which tools are installed, which os, what version a tool reports. The vim
-- functions they ask are stubbed to fixed answers so that the test sees the same
-- configs everywhere -- otherwise the commands would carry the absolute tool paths
-- of whichever machine ran it, and differ between a laptop and CI.
--
-- The answers say: no tool is installed anywhere, this is not windows, and no tool
-- replies to a version query. So commands come out with bare binary names, and the
-- three configs that branch on the platform are read in their unix form.

local plugin = assert(_G.arg[1], 'usage: nvim --headless -l efmls-extract.lua <efmls-configs-nvim dir>')

local realfn = vim.fn
vim.fn = setmetatable({
  -- fs.executable falls back to the plain binary name when it can find neither a
  -- project-local nor a global one
  executable = function()
    return 0
  end,
  filereadable = function()
    return 0
  end,
  has = function()
    return 0
  end,
  system = function()
    return ''
  end,
}, { __index = realfn })

package.path = table.concat({
  plugin .. '/lua/?.lua',
  plugin .. '/lua/?/init.lua',
  package.path,
}, ';')

-- a host that has efmls-configs installed for its own use may have loaded some of
-- it during startup, before the stubs above were in place, and require would hand
-- back that cached copy -- which is how an absolute tool path gets into a snapshot
for module in pairs(package.loaded) do
  if module:match('^efmls%-configs') then
    package.loaded[module] = nil
  end
end

local configs = {}
for _, kind in ipairs({ 'linters', 'formatters' }) do
  local dir = plugin .. '/lua/efmls-configs/' .. kind
  for entry, entrytype in vim.fs.dir(dir) do
    local tool = entry:match('^(.+)%.lua$')
    if entrytype == 'file' and tool then
      local module = string.format('efmls-configs.%s.%s', kind, tool)
      local ok, config = pcall(require, module)
      if not ok then
        error(string.format('%s: %s', module, config), 0)
      end
      configs[kind .. '/' .. tool] = config
    end
  end
end

assert(next(configs) ~= nil, 'no configs found in ' .. plugin)

io.write(vim.json.encode(configs))
