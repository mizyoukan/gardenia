gardenia
========

CLI downloader of Vim plugins from GitHub.
This tool keeps only plugins to be latest version.

Usage:
------

Create `gardenia.json` into vimfiles directory
(Windows: `%UserProfile%\vimfiles`, other: `$HOME/.vim`).
Content is map of `directory: owner/repo`, example is following:

```json
{
    "bundle": [
        "sunaku/vim-unbundle",
        "mattn/emmet-vim"
    ],
    "ftbundle": {
        "go": "vim-jp/vim-go-extra",
        "javascript": [
            "jelera/vim-javascript-syntax",
            "jiangmiao/simple-javascript-indenter"
        ]
    }
}
```

And execute `gardenia` then start installing.

### Options

option | default value     | explanation
--     | --                | --
c      | ~/.cache/gardenia | Cache directory path
e      | false             | Clean not       managed plugins
f      | false             | Force reinstall plugins
l      | false             | Only  list      plugins to install

Install:
--------

```sh
go get github.com/mizyoukan/gardenia
```

License:
--------

MIT License

Author:
-------

Tamaki Mizuha <mizyoukan@outlook.com>
