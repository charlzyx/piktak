class Piktak < Formula
  desc "Connect remote clients to services running on your machine"
  homepage "https://github.com/charlzyx/piktak"
  version "0.1.0"
  license :cannot_represent

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.0/piktakd-darwin-arm64"
      sha256 "981733d644f3f7c71ecb8b45937b16aa580c614d8d36016669df3a3ab2e84354"
    else
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.0/piktakd-darwin-amd64"
      sha256 "0b460d147bac640ef2f84bda1b32e72b5a07f04ad5b1756a9fa47139bd947110"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.0/piktakd-linux-arm64"
      sha256 "71faf26c188be1ce8650a7325edab3ae3ad358e174ec084a557f5ae6ec3bc1fe"
    else
      url "https://github.com/charlzyx/piktak/releases/download/v0.1.0/piktakd-linux-amd64"
      sha256 "de8d711f1f9f034d28e60d2ce1b18cffbd432146ef93a4248060b1d2acd36c0d"
    end
  end

  def install
    bin.install Dir["piktakd-*"].first => "piktakd"
  end

  service do
    run [opt_bin/"piktakd", "-config", "#{Dir.home}/.config/piktak/config.yml"]
    keep_alive true
  end

  test do
    assert_predicate bin/"piktakd", :executable?
  end
end
