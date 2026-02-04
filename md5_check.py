import os
import hashlib

def calculate_md5(file_path):
    """计算文件的MD5哈希值"""
    hash_md5 = hashlib.md5()
    with open(file_path, "rb") as f:
        for chunk in iter(lambda: f.read(4096), b""):
            hash_md5.update(chunk)
    return hash_md5.hexdigest()

def main():
    # 目录路径
    send_files_dir = "cmd/send_files"
    received_files_dir = "cmd/received_files"

    # 检查目录是否存在
    if not os.path.exists(send_files_dir):
        print(f"Directory {send_files_dir} does not exist.")
        return
    
    if not os.path.exists(received_files_dir):
        print(f"Directory {received_files_dir} does not exist.")
        return

    # 获取两个目录中的所有文件路径（相对路径）
    send_files = {}
    received_files = {}
    
    # 收集发送文件夹中的文件
    for root, dirs, files in os.walk(send_files_dir):
        for file in files:
            file_path = os.path.join(root, file)
            # 获取相对于send_files_dir的相对路径
            rel_path = os.path.relpath(file_path, send_files_dir)
            send_files[rel_path] = file_path
    
    # 收集接收文件夹中的文件
    for root, dirs, files in os.walk(received_files_dir):
        for file in files:
            file_path = os.path.join(root, file)
            # 获取相对于received_files_dir的相对路径
            rel_path = os.path.relpath(file_path, received_files_dir)
            received_files[rel_path] = file_path

    # 找出两个目录中都存在的文件
    common_files = set(send_files.keys()) & set(received_files.keys())
    
    print("MD5 comparison results:")
    print("=" * 80)
    
    all_match = True
    match_count = 0
    mismatch_count = 0
    missing_count = 0
    
    # 比较同名文件的MD5
    for rel_path in sorted(common_files):
        send_file_path = send_files[rel_path]
        received_file_path = received_files[rel_path]
        
        send_md5 = calculate_md5(send_file_path)
        received_md5 = calculate_md5(received_file_path)
        
        if send_md5 == received_md5:
            print(f"✓ MATCH:    {rel_path}")
            print(f"  Send MD5:    {send_md5}")
            print(f"  Receive MD5: {received_md5}")
            match_count += 1
        else:
            print(f"✗ MISMATCH: {rel_path}")
            print(f"  Send MD5:    {send_md5}")
            print(f"  Receive MD5: {received_md5}")
            mismatch_count += 1
            all_match = False
    
    # 找出只在send_files中存在的文件
    send_only = set(send_files.keys()) - set(received_files.keys())
    if send_only:
        print(f"\nFiles only in send_files ({len(send_only)} files):")
        for rel_path in sorted(send_only):
            print(f"  - {rel_path}")
        missing_count += len(send_only)
        all_match = False
    
    # 找出只在received_files中存在的文件
    receive_only = set(received_files.keys()) - set(send_files.keys())
    if receive_only:
        print(f"\nFiles only in received_files ({len(receive_only)} files):")
        for rel_path in sorted(receive_only):
            print(f"  - {rel_path}")
        missing_count += len(receive_only)
        all_match = False
    
    # 输出统计信息
    print("\n" + "=" * 80)
    print("SUMMARY:")
    print(f"Total common files compared: {len(common_files)}")
    print(f"Files with matching MD5:     {match_count}")
    print(f"Files with different MD5:    {mismatch_count}")
    print(f"Files missing in one folder: {missing_count}")
    
    if all_match and len(common_files) > 0 and missing_count == 0:
        print("\n✓ SUCCESS: All files match!")
    else:
        print("\n✗ ISSUES FOUND:")
        if mismatch_count > 0:
            print(f"  - {mismatch_count} files have different content")
        if missing_count > 0:
            print(f"  - {missing_count} files are missing in one folder")

if __name__ == "__main__":
    main()